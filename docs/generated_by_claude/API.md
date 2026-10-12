# Open Ads 接口契约设计（API）

状态：设计提案，尚未进入开发。本文为提交版接口契约，供服务实现、接口集成和契约测试使用；所有 API 由 SSP、DSP、DMP 各自拥有 DTO、校验器、版本策略和测试，禁止跨服务复用领域类型。具体路径和字段仍可经设计变更评审调整，但不得弱化本文定义的协议、安全与幂等边界。

## 0. 文档定位与术语

本文定义 SSP、DSP、DMP 的 HTTP/OpenRTB 接口契约，包括调用方、请求/响应、版本、错误语义、安全要求和兼容规则。系统目标与服务职责见 [README.md](README.md)，跨服务架构与交互见 [HLD.md](HLD.md)，领域模型、持久化和状态机实现约束见 [LLD.md](LLD.md)。本文件只定义接口边界，不重新定义领域事实或改变 OpenRTB 标准语义。

领域术语以 [README.md](README.md) §0.2 为准；本表只定义接口层术语，不重复定义 reservation、billable event 等领域概念：

| 术语 | 接口语义 |
|---|---|
| API version | 自有 JSON API 以路径版本标识；不兼容变更升主版本，OpenRTB 版本按 OpenRTB 契约协商和校验 |
| no-bid 表达 | 领域语义见 README §0.2；OpenRTB endpoint 按协议表达，SDK Client API 映射为 `no_fill`，不代表传输错误 |
| idempotency identifiers | `notice_id` 去重 notice 投递/记录；`reservation_id` 标识预算预留；`source_event_key` 标识账务事件；`Idempotency-Key` 用于自有 API 的逻辑操作。各 ID 按职责独立、跨对应重试稳定，不可互换 |
| callback profile | 双方约定的 notice HTTP 方法、宏展开、payload、超时、重试和成功响应；不能假设 OpenRTB 为 notice 定义通用 JSON body |
| error envelope | 自有 JSON API 的 `{code, message, request_id}` 错误结构；OpenRTB endpoint 遵循 OpenRTB no-bid 与 HTTP 语义，不套用该结构 |

若本文件的接口级说明与 README/HLD/LLD 的领域或状态语义发生冲突，应按设计变更流程先统一文档，再实现接口；不得通过接口实现单方面改变共享语义。

## 1. 通用约定

### 1.1 基础约定

| 项目 | 约定 |
|---|---|
| 网络 | HTTPS；管理 API 不暴露公网 |
| JSON API 版本 | 路径版本，例如 `/sdk/v1`、`/internal/v1`、`/admin/v1`；不兼容变更升主版本 |
| 请求关联 | 接收/返回 `X-Request-ID`；没有时由服务生成；允许 `traceparent` 贯穿调用链 |
| 时间 | RFC 3339 UTC；事件同时保留 `occurred_at` 和服务端 `received_at` |
| 金额 | OpenRTB wire `price` 是十进制 CPM；领域/数据库 `*_micros` 使用整数主币微单位（1 主币 = 1,000,000 micros），并单独记录 ISO-4217 currency；不得用 binary float 累计 |
| 幂等 | 按操作类型使用稳定标识：`notice_id` 用于 notice 去重、`reservation_id` 与 `source_event_key` 用于账务结算校验、`Idempotency-Key` 用于自有 API 操作；重试复用对应标识，不得互换 |
| 错误 | 自有 JSON API 使用 `{code, message, request_id}`；不回显堆栈、敏感标识或内部 SQL。OpenRTB 接口按 OpenRTB no-bid/HTTP 语义返回，不包通用错误信封 |
| 分页 | 管理 API 使用 `limit` + opaque `cursor`；响应包含 `items`、`next_cursor` |
| 并发更新 | 管理资源请求/响应 body 中包含 `version` 字段（64-bit 整数，自增）；更新请求体必须携带当前最新 `version`，版本冲突返回 `409`。MVP 阶段不使用 HTTP `ETag` / `If-Match` header（属于 API gateway 生产级特性，生产接入后启用） |

常见状态：`202` 异步操作已接受但尚未完成；`400` 请求无效；`401` 未认证；`403` 无权限或租户不匹配；`404` 管理资源不存在；`409` 版本冲突或非法状态迁移；`413` 超过请求体上限；`422` 业务规则不满足；`429` 限流；`503` 依赖不可用。实时 bidder 超时按 no-bid 处理，不等待管理面错误细节。

### 1.2 认证与授权

默认拒绝。所有凭据支持轮换（双版本并存窗口），日志不记录凭据；IP allowlist 只作补充，不作为唯一认证手段。自有 JSON API 的认证失败返回错误信封，OpenRTB endpoint 返回空 body 的 `401`/`403`。

| 接口类别 | 调用方 | 认证 | 授权与租户解析 |
|---|---|---|---|
| `POST /openrtb2/auction` | Supply | endpoint 凭据：`Authorization: Bearer <credential>` + `X-OpenAds-Endpoint-ID`；SSP 只保存凭据哈希（`supply_endpoint.credential_hash`） | 凭据 → `supply_endpoint` → property/tenant；请求中的 site/app 与 endpoint 绑定不一致则拒绝 |
| `POST /openrtb2/bid` | SSP | mTLS 客户端证书（服务身份） | DSP 把证书主体映射到已登记的 SSP partner 与 DSP tenant；不信任请求体中的任何 tenant 声明 |
| 内部 REST（SSP→DMP、DSP→SSP creative-registry、`subject-tokens:derive` 等） | 服务 | mTLS | 接收方维护「调用方主体 → 允许的 tenant/操作」授权表；请求体 `tenant_id` 超出授权范围返回 `403` |
| notice 回调 `nurl`/`lurl`/`burl` | SSP | mTLS，或请求签名：`X-OpenAds-Timestamp` + `X-OpenAds-Signature` = HMAC-SHA256(method ‖ path ‖ sha256(body) ‖ timestamp)，时间窗 ±5 分钟；由接入 profile 选定 | 签名只证明调用方身份；预算授权仍由 `reservation_ref` 校验，两者缺一不可 |
| `/admin/v1/*` | 管理台/脚本 | OIDC bearer token，含 `tenant_id` 与 `roles` 声明 | `admin`（读写）、`operator`（配置读写，无预算调整与隔离处置）、`viewer`（只读）；资源 tenant 必须匹配声明；所有写操作产生审计事件 |
| `/sdk/v1/*` | Portal/JS SDK | 无用户凭据：`ad-requests` 依赖 placement + Origin allowlist + 限流；`impressions`/`events` 依赖 event token；撤回依赖 `revoke_token` | tenant 一律由 placement/token 解析，不接受客户端指定 |
| `GET /healthz`、`/readyz` | 负载均衡/探针 | 网络层隔离 | 不返回业务数据 |

### 1.3 配置发布语义

- **SSP**：publisher/property/placement/auction-policy/supply-endpoint/dsp-endpoint 没有草稿态，管理 API 的写事务（含业务校验）提交即“发布”，同事务写 `ssp_config_outbox`；校验失败返回 `422`，不写 outbox。
- **DSP**：campaign/line-item/creative 有 `draft`/`ready`/`active`/`paused` 状态。`POST /admin/v1/campaigns/{id}:publish` 发布该 campaign 下所有 `ready` line item 及其引用 creative 的一致版本。对已发布对象的单独 `PUT`（如只改 creative）会自动重新校验并发布受影响的 campaign；校验失败返回 `422` 并保留旧快照继续服务。

### 1.4 Idempotency-Key

创建资源的 `POST`、`:publish`、`:retry`、`:resolve` 必须带 `Idempotency-Key` header。作用域为 (tenant, method, path, key)，保留 24 小时。同 key 且 body 哈希一致：重放首次响应；同 key 但 body 不同：`409 idempotency_key_conflict`；首次请求仍在处理：`409 idempotency_in_progress`。该 key 只用于自有 API 的逻辑操作，不与 `notice_id`、`reservation_id`、`source_event_key` 混用。

### 1.5 错误码目录

自有 JSON API 的 `code` 取值固定为下表，新增须经契约评审。

| HTTP | code | 含义 |
|---|---|---|
| 400 | `invalid_request` / `invalid_asset_ref` | 请求结构无效 / asset_ref 不符合格式 |
| 401 / 403 | `unauthenticated` / `forbidden` | 未认证 / 无权限或租户不匹配 |
| 404 | `not_found` / `asset_not_found` | 资源不存在 / asset 未注册 |
| 409 | `version_conflict` / `invalid_state_transition` / `idempotency_key_conflict` / `idempotency_in_progress` | 乐观锁冲突 / 非法状态迁移 / 幂等冲突 |
| 413 | `payload_too_large` | 超过请求体上限 |
| 422 | `validation_failed` / `unsupported_mime_type` / `asset_hash_mismatch` / `asset_too_large` / `asset_dimensions_exceeded` | 业务规则不满足 |
| 429 | `rate_limited` | 限流，带 `Retry-After` |
| 503 | `dependency_unavailable` / `policy_state_unavailable` / `asset_unavailable` / `asset_tampered_detected` | 依赖不可用 / 撤回 gate 不可用 / 素材不可读 / 素材校验失败 |

### 1.6 机读契约

每个服务在其仓库维护 OpenAPI 3.1 文件（`api/openapi/<service>.yaml`），OpenRTB `ext.openads`、`burl` JSON body、event token 载荷另以 JSON Schema 描述。契约测试从这些文件生成，CI 检查向后兼容；本文是语义说明，机读文件是契约测试的执行基准。

## 2. SSP API

SSP 对外提供 OpenRTB exchange endpoint、浏览器 Client API 和受保护管理 API；对 DSP/DMP 的调用由 SSP 发起。RTB handler 读预热内存/Valkey 缓存，不直接读 `ssp_db`。

### 2.1 Supply OpenRTB endpoint

| Method / path | 调用方 | 请求/响应 | 语义 |
|---|---|---|---|
| `POST /openrtb2/auction` | Supply | OpenRTB 2.6 `BidRequest` → `BidResponse`/no-bid | 先按 §1.2 认证 supply endpoint 并解析 tenant；校验版本、request/imp ID、媒体能力与截止时间；验证/补齐 `source.schain`；以 `tmax` 限定总延迟 |

响应中的竞价结果和通知 URL 遵循 OpenRTB 2.6。入口不把 Client SDK DTO 当作 OpenRTB 请求。

Supply 直连路径的创意交付：返回的 bid 不透传 DSP 的 `adm`；SSP 将 `bid.adm` 设为指向 SSP 托管 `render_url` 的 `<iframe sandbox>` 片段，并在 `bid.ext.openads` 中携带 `render_url`、`click_url`、`expires_at`。该 iframe 与 SDK 路径共用 `/render/v1/` 与 `/sdk/v1/impressions` 链路，billable 触发和账务规则一致；首期 SSP 不向 Supply 提供 `burl`，也不做 Supply 侧计费对账。

### 2.2 Browser Client API

| Method / path | 调用方 | 请求/响应 | 语义 |
|---|---|---|---|
| `POST /sdk/v1/ad-requests` | Portal/JS SDK | placement、slot、去敏 page context、consent fixture → SDK AdResponse/no-fill | SSP 内部创建 auction 并映射到 OpenRTB；`Cache-Control: no-store` |
| `POST /sdk/v1/identity` | JS SDK/Publisher page | 可选 publisher pseudonymous subject → SSP-scoped subject ID、expiry、status | 明确 opt-in；ID 按 publisher scope 隔离并存 first-party storage；无 consent 返回 `identity: null` |
| `POST /sdk/v1/identity-sync` | JS SDK/Publisher page | subject ID、partner ID → allowlisted one-time sync URL/state | 可选 legacy/browser match；只允许登记 partner 和固定 return URL |
| `POST /sdk/v1/identity:revoke` | SDK/Publisher privacy flow | subject ID、`revoke_token`、reason、request ID → `pending`/`completed` 与 revocation ID | SSP 先阻止新 identity-bearing auction；仅在 SSP 活跃 auction 与 DMP in-flight lookup 均 drain、撤回状态持久化后报告 completed；pending 期间不发送身份/受众 |
| `GET /sdk/v1/identity-revocations/{revocation_id}` | SDK/Publisher privacy flow | revocation ID → 当前 `pending`/`completed` 状态 | 凭 `revoke_token` 或 publisher 服务端凭据（§1.2）查询，不返回 tombstone 或其他用户数据 |
| `POST /sdk/v1/events` | JS SDK | client event ID、event type、token、发生时间 → 接收状态 | 仅作为客户端测量/调试信号；不得单独触发财务 ledger |
| `POST /sdk/v1/impressions` | SSP 托管的 sandbox render iframe | event ID、token、发生时间 → accepted/duplicate/rejected | 校验 auction/bid/imp/placement 绑定、expiry 和一次性约束；仅 sandbox profile 可触发 payable ledger，不构成生产 IVT 证明 |
| `GET /click/v1/{click_token}` | Browser | opaque token → 记录 click 并 302 到竞价记录中的 allowlisted landing URL | 不接受客户端 redirect 参数；click 不计费；重复请求去重 |
| `GET /render/v1/{render_token}` | Browser iframe | opaque token → SSP 托管的 sandbox HTML 壳文档 | 校验 token 与已胜出 creative 绑定；文档内含 `<img src=asset_url>` 与 impression 上报脚本；响应 `Cache-Control: no-store`、`Referrer-Policy: no-referrer`；CSP 仅允许该图片资源和向 SSP impressions/events 端点的请求（脚本以 hash 放行），`frame-ancestors` 限定为 allowlisted Portal/publisher origin |
| `GET /asset/v1/{asset_token}` | Browser（HTML 壳内 `<img>`） | opaque token → 图片字节 | token 由 render 流程签发，绑定同一 creative/asset hash；服务端代理读取不可变 asset store 并重算 hash；响应头见 LLD §3.2.1.3；asset 不可用返回 `503 asset_unavailable` |

`ad-requests` 示例：

```json
{
  "request_id": "client-generated-uuid",
  "placement_id": "018f7a3b2c9d7e1fa3b4c5d6e7f89012",
  "slot": { "id": "leaderboard", "width": 728, "height": 90 },
  "page": { "origin": "https://publisher.test", "path": "/demo" },
  "consent": { "source": "test-fixture", "value": null }
}
```

filled response：

```json
{
  "request_id": "client-generated-uuid",
  "status": "filled",
  "ad": {
    "creative_id": "018f7a3b2c9d7e1fa3b4c5d6e7f89034",
    "format": "image",
    "render_url": "https://render.test.example/render/v1/signed/short-lived-token",
    "click_url": "https://ssp.test.example/click/v1/opaque-token",
    "event_token": "short-lived-opaque-token",
    "expires_at": "2026-10-01T12:00:30Z"
  },
  "diagnostics": {
    "auction_id": "opaque-test-id",
    "outcome": "bid",
    "duration_ms": 42
  }
}
```

身份 opt-in 请求：

```json
{
  "placement_id": "018f7a3b2c9d7e1fa3b4c5d6e7f89012",
  "publisher_subject": {
    "namespace": "publisher-account-v1",
    "value": "publisher-controlled-pseudonymous-value"
  },
  "consent": { "purpose": "personalized_ads", "receipt_id": "cmp-receipt-ref" }
}
```

响应只返回 SSP 生成的 opaque `subject_id`、`scope`、`expires_at` 和 `revoke_token`，不回显 publisher ID。`revoke_token` 与该 subject 绑定、有效期不超过 subject 过期时间，只能用于撤回与撤回状态查询，不得用于其他接口。`publisher_subject.value` 仅用于匹配，SSP 只保存带独立 HMAC key 的索引，不保留明文；没有 publisher subject 时，可由 SSP 生成随机 subject ID 并在 consent 允许时由 SDK 保存于 publisher first-party storage。

no-fill 使用 `200` + `{"request_id":"...","status":"no_fill"}`。错误响应包含 `code`、`request_id` 和对客户端安全的简短 `message`。无广告不是 transport error。测试 diagnostics 默认不返回 bidder、用户或完整 OpenRTB 详情。

首期 renderer 只输出 SSP 托管的静态 PNG/JPEG/WebP image creative，不执行 DSP HTML/JavaScript。Impression 请求只携带 event token、稳定 `event_id` 和 `occurred_at`；SSP 从 event token 的服务端认证载荷（AEAD，客户端不可读写）取得金额、currency、DSP、`burl` 目标和 reservation，不依赖任何缓存中的竞价记录。Sandbox profile 中第一个有效 impression 在同一 `ssp_db` 事务写入唯一 billable delivery event、publisher payable ledger 和 DSP billable outbox；之后由 outbox 按 DSP `burl` 的 sandbox JSON POST profile 重试。Publisher payable 暂为该有效 DSP billable amount 的 100%，不代表生产 payout。普通 `/sdk/v1/events` 与 click 永不触发财务分录。

### 2.3 Portal 与 SSP 管理 API

| Method / path | 调用方 | 主要请求/响应 | 语义（补充） |
|---|---|---|---|
| `GET /admin/v1/publishers` / `POST /admin/v1/publishers` | SSP 管理台 / 管理脚本 | Publisher 配置 DTO；响应包含 ID/version | — |
| `GET /admin/v1/properties` / `POST /admin/v1/properties` | 同上 | Property 配置与 Publisher 外部 ID | — |
| `GET /admin/v1/placements` / `POST /admin/v1/placements` | 同上 | `auction_policy_id`、格式、floor、币种、隐私/计费策略版本 | placement 关联已发布的 AuctionPolicy |
| `GET /admin/v1/auction-policies` / `POST /admin/v1/auction-policies` / `PUT /admin/v1/auction-policies/{id}` | 同上 | `at`、`tie_breaker`、`floor_enforcement`、`currency_policy`；响应包含 ID/version | 策略发布后由 placement 引用；MVP 仅支持 `at=1`、已定义的 tie-break 与 exact-match 币种策略 |
| `GET /admin/v1/supply-endpoints` / `PUT /admin/v1/supply-endpoints/{id}` | 同上 | Supply 协议 profile、ads.txt/app-ads.txt seller/account 映射、schain 策略和状态 | — |
| `GET /admin/v1/dsp-endpoints` / `POST /admin/v1/dsp-endpoints` / `PUT /admin/v1/dsp-endpoints/{id}` | 同上 | `dsp_endpoint_id`、bid URL、OpenRTB profile、`callback_host_allowlist`、`priority`、`timeout_ms`、status、version | `callback_host_allowlist` 用于 SSRF 防护；`priority` 越小排序越优先，仅作为同价 tie-break |
| `GET /admin/v1/dsp-endpoints/{id}/creative-whitelist` | 同上 | `whitelist: [{creative_id, asset_ref, asset_sha256, width_px, height_px, mime_type, landing_url_host, status, version}]` | 只读查询；白名单唯一写入路径为 DSP → SSP 内部同步 API；`asset_sha256` 为 64 位小写 hex |
| `GET /admin/v1/config-outbox` | 同上 | `?status=pending|failed` + limit/cursor → 未发布 / 发布失败的 outbox 事件列表 | 管理台 UI 展示"配置发布进度与失败项"，配合 retry 手动重放 |
| `POST /admin/v1/config-outbox/{event_id}:retry` | 同上 | event_id → `{status: accepted}` | 对 failed_attempts >= 5 的 outbox 手动重放，重置失败计数 |
| `POST /internal/v1/identity-links:complete` | DSP identity-sync callback | sync token、DSP-specific user ID、consent receipt/version、expiry → link status/version | 将 DSP ID 映射为当前 SSP subject 的 pairwise `buyeruid`；不得接受任意 user ID upsert；sync token 生命周期绑定 partner+subject+consent，仅可消费一次 |
| `POST /internal/v1/subject-tokens:derive` | 已授权 DMP 数据提供方的导入控制器 | `namespace`、`key_version`、`values[]`（publisher 自控的伪名值，批量上限受限）→ `tokens[]`（与输入同序） | 用与 SSP 在线路径相同的两步 HMAC 派生 DMP `subject_token`（见 LLD §2.2.1.1）；HMAC 密钥不外发；不记录、不回显原值；按 provider/namespace 授权、限流并审计 |
| `DELETE /admin/v1/identity-links/{partner_id}/{subject_id}` | SSP 管理台隐私管理 tab | deletion reason/consent version → revocation status | 撤回后停止下发，并按保留策略清理/失效映射及缓存 |
| `POST /internal/v1/dsp-endpoints/{dsp_endpoint_id}/creative-registry:upsert` | DSP → SSP（creative 白名单自动同步） | `tenant_id`、`creatives: [{creative_id, asset_ref, asset_sha256, width_px, height_px, mime_type, landing_url_host, status, version}]` → `{accepted_count, rejected: [{creative_id, reason}]}` | DSP 管理台点"同步到 SSP"时批量上报；记录字段与管理 API 一致；SSP 按 endpoint + creative_id 幂等更新 |
| `GET /healthz` / `GET /readyz` | 运维探针 / 负载均衡 | 存活、配置快照就绪版本；不返回密钥或业务数据 | readyz 失败时摘除该实例的 RTB 流量（SSP bid handler、browser SDK handler、internal identity）；管理 API 与 refresh worker 即使 readyz 失败仍继续处理配置发布。 |

SSP 管理变更写 `ssp_db` 与配置 outbox，由异步刷新器（见 LLD §2.4.1）更新 Valkey/进程快照；管理 API 不直接改缓存作为唯一写入。

### 2.4 SSP 发起的下游 API

| Method / path | 接收方 | 协议 | 主要内容 |
|---|---|---|---|
| `POST {dsp.bid_url}` | DSP | OpenRTB 2.6 | 标准 BidRequest → BidResponse/no-bid；deadline 不超过 SSP 剩余 auction budget |
| `POST /internal/v1/audience:resolve` | DMP | JSON | pseudonymous token + consent/purpose → provider/segment IDs |
| `POST /internal/v1/events:batch` | DMP | JSON | 去敏事件批次；at-least-once、稳定 event ID |

DSP 返回的 `nurl`/`lurl`/`burl` 只允许命中该 DSP 注册并验证过的 callback host/egress policy（由 §2.3 DSP endpoint 的 `callback_host_allowlist` 字段配置），防止 SSP 被利用为 SSRF proxy。

### 2.5 User ID sync 规则

- SDK 可以在明确 consent 后申请/携带 publisher-scoped opaque subject；没有合法标识时不创建、读取或发送 identity。
- 如使用 cookie/redirect sync，必须是已登记 partner 的单次、短期、有状态流程，并回调到 `identity-links:complete`；禁止开放任意 redirect URL。浏览器第三方 cookie 不可用时不降级为 fingerprinting。
- `identity-sync` response 只给固定 partner URL 加不透明的一次性 state；state 绑定 subject、partner、consent scope、expiry 且只能消费一次（校验逻辑在 SSP identity-links:complete handler 内）。禁止在 URL query 或 referer 中携带原始 ID。
- SSP 在自己的 `ssp_db` 保存 publisher subject 与每个 DSP buyer ID 的 pairwise link；只向对应 DSP 发送其 link。OpenRTB `user.id` 保持 exchange-scoped，`buyeruid` 是该 DSP 的 buyer-specific mapping；经 provider 合约和 consent 允许的 EID 放入 `user.eids`。
- SSP 给 DMP 发送按 tenant、publisher namespace、key version 计算的伪名 HMAC token；DMP 不需要知道 DSP buyer ID，也不回传 DMP 内部 subject ID。DMP 只返回 segment IDs。
- revoke/expiry 后不再使用 link；缓存立即失效。请求和日志不记录原始 ID、完整 consent string 或 sync token。
- 第三方 cookie/redirect sync 仅作为已签约 partner 的兼容选项；默认优先 publisher first-party ID 或标准 EID。浏览器无法完成 sync 时省略 `buyeruid`，不做概率匹配或设备指纹。

## 3. DSP API

DSP 的 bidder endpoint 与 advertiser/campaign 管理 API 分开部署或分开授权。Bid 热路径只读进程/Valkey 快照；不访问 MariaDB。

### 3.1 OpenRTB bidder 与 notice callback

| Method / path | 调用方 | 请求/响应 | 语义 |
|---|---|---|---|
| `POST /openrtb2/bid` | SSP/exchange | OpenRTB 2.6 BidRequest → BidResponse 或 no-bid | 按 request deadline 和已加载快照决策；不得在 cache miss 时回源 MariaDB |
| 按 bidder 返回的 `nurl` 调用（win notice） | SSP/exchange | GET 或 POST；按 OpenRTB 宏替换（`${AUCTION_PRICE}` 等） | 仅记录非计费 win，不扣 advertiser spend，也不改变/释放 held reservation；reservation 仅由互斥的 `lurl` loss 或 `burl` billable 终态结算 |
| 按 bidder 返回的 `lurl` 调用（loss notice） | SSP/exchange | GET 或 POST；loss code macro `${AUCTION_LOSS}` 已替换 | 记录 loss 状态；在同一事务中释放 reservation 的 held 额度 → 退回全局预算池（leased_total 下降、available 上升）；若该 reservation 已有 billable 终态，则按终态冲突隔离，不释放 |
| 按 bidder 返回的 `burl` 调用（billable notice） | SSP/exchange | MVP：HTTPS POST JSON（body 见下方示例）；reservation_ref 与 source_event_key 在 body 中传递 | 校验 event/auction/bid/imp、reservation、币种/金额上限后，在事务中写 advertiser spend ledger 并更新所有适用预算范围的 spent_micros（reservation held 转为 spent_micros，不重复加回全局池）；重复 event 幂等成功 |
| `GET /healthz` / `GET /readyz` | 运维探针 | 无业务 body | 报告服务存活、配置版本/预算租约可用状态 |

通知 URL 由 DSP 在 BidResponse 对应 bid 的 `nurl`、`lurl`、`burl` 提供。OpenRTB 不定义通用 JSON notice payload；双方 MVP profile 的方法、宏、超时、重试、成功响应及 reservation/source event ID 按 LLD §2.2 与 §3.1.2 的幂等规则执行。billing price 宏的替换和结算金额按上述 profile 确定，不以 win price 自动替代 billable price。SSP 仅调用已登记 bidder 的合法 HTTPS host（由 §2.3 DSP endpoint 的 callback_host_allowlist 约束），并校验重定向/解析后的目标，避免 SSRF。

MVP `burl` JSON body：

```json
{
  "event_id": "stable-billable-event-id",
  "auction_id": "auction-id",
  "bid_id": "bid-id",
  "imp_id": "imp-id",
  "reservation_ref": "signed-reservation-reference",
  "amount_micros": 2500,
  "currency": "USD",
  "occurred_at": "2026-10-07T12:00:00Z"
}
```

SSP 只在 payable transaction/outbox commit 后投递；重试复用 `event_id` 和相同 body。DSP 校验金额不超过 reservation 上限且 currency 一致。首期金额按 first-price CPM 单次展示金额计算，并按已定义的 half-even 规则舍入。

回执约定：DSP 仅在账务事务提交后返回 `204`；相同 `event_id` 且内容一致的重复 notice 也返回 `204`，同 ID 不同内容返回 `409` 并隔离。schema 或 reservation 无效时返回 `4xx` 并隔离，不自动重试；网络错误、`429`、`5xx` 按 `Retry-After` 或带抖动的指数退避重试，直到 reservation grace 截止。截止仍未确认的 outbox 项保留并告警/对账，不静默丢弃。MVP 的 placement、DSP bid、creative 及 notice currency 必须完全一致，不做汇率换算。

### 3.2 DSP 管理/控制 API

管理 API 使用 version 做乐观锁；变更事务写入 `dsp_db` 和 config outbox，再由后台发布至 Valkey/节点快照。

| Method / path | 用途 | 主要数据 |
|---|---|---|
| `GET/POST /admin/v1/advertisers`、`PUT /admin/v1/advertisers/{id}` | 广告主管理 | 状态、允许币种 |
| `GET/POST /admin/v1/campaigns`、`PUT /admin/v1/campaigns/{id}` | Campaign 管理 | 状态、时间窗、目标 |
| `GET/POST /admin/v1/line-items`、`PUT /admin/v1/line-items/{id}` | 投放与预算配置 | targeting、bid、daily/lifetime budget、币种 |
| `GET/POST /admin/v1/creatives`、`PUT /admin/v1/creatives/{id}` | 素材与审核 | format、asset reference/hash、server-derived width/height、review state |
| `POST /admin/v1/creative-assets` | DSP 管理台 | multipart PNG/JPEG/WebP upload | 校验 MIME、文件格式、尺寸与大小后写入不可变 asset store；返回 `asset_ref`、SHA-256、解码所得宽高，不返回可公开长期访问的 bucket URL |
| `POST /admin/v1/campaigns/{id}:publish` | 发布配置版本 | 校验配置并返回 `config_version`、发布状态 |
| `GET /admin/v1/budgets/{line_item_id}` | 查看预算账本汇总 | spent/reserved/available、currency、ledger version |
| `GET /admin/v1/notice-quarantine` | 查看被隔离的 notice | `?status=open/written_off/adjusted`、limit/cursor → 隔离项（reason、reservation_id、脱敏 payload 摘要、状态） |
| `POST /admin/v1/notice-quarantine/{id}:resolve` | 处置隔离项 | `resolution`（`written_off`/`adjusted`）、`note`，须带 `Idempotency-Key` | `adjusted` 写 ledger adjustment 分录，不修改 `budget_account`，超支额单独对账；首期默认 `written_off`；仅 `admin` 角色 |

管理 API 不允许客户端直接调整 `spent` 汇总字段。财务调整必须生成带原因的新 ledger entry，并记录关联的审计事件。

首期 `line-items` 写入 DTO 至少包含 `campaign_id`、状态、`flight_start_date`（UTC 日期，创建后不可修改，作为 lifetime 预算的 `period_start`）、`flight_end_date`（可选）、`bid_cpm_micros`、currency、daily/lifetime budget、`targeting` 和关联 creative IDs。daily budget 按 UTC 自然日聚合，`period_start` 为 UTC 日期，`00:00 UTC` 开始新 scope；不接受租户本地时区配置。Targeting 仅支持 `site_domains`、`page_path_prefixes`、`banner_sizes`；同数组内 OR，指定维度间 AND。domain 为规范化 host 精确匹配，path prefix 按路径段边界匹配，size 精确匹配；省略维度表示不限。未知字段或不支持条件在 publish 时返回 `422`，成功发布生成不可变 targeting version。首期 creative 只接受受控 asset store 的静态 PNG/JPEG/WebP 与 HTTPS landing URL，不接受任意 HTML/JS。

Asset store 是无领域模型的共享对象存储基础设施：DSP 控制面只写、SSP renderer 只读。对象不可覆盖，`asset_ref` 是不透明 key；上传时校验实际文件签名与解码尺寸。创建 creative 时服务端从 asset metadata 复制宽高与 hash 到 creative 记录，不接受调用者自报尺寸；publish 时复核 asset hash、MIME 和尺寸。OpenRTB sandbox profile 在 `seatbid[].bid[].ext.openads` 中携带 `asset_ref`、`asset_sha256`、`asset_width_px`、`asset_height_px` 和 `landing_url`；SSP 校验其与注册 creative 的 metadata 一致后绑定到短时 render token，不执行 `adm` 中的任意 HTML/JavaScript。Renderer 通过服务端只读凭据取图，不将对象存储凭据暴露给浏览器。

```json
{
  "campaign_id": "018f7a3b2c9d7e1fa3b4c5d6e7f89056",
  "status": "active",
  "flight_start_date": "2026-10-01",
  "bid_cpm_micros": 2500000,
  "currency": "USD",
  "daily_budget_micros": 1000000000,
  "lifetime_budget_micros": 10000000000,
  "targeting": {
    "site_domains": ["publisher.test"],
    "page_path_prefixes": ["/sports"],
    "banner_sizes": [{"w": 300, "h": 250}]
  },
  "creative_ids": ["018f7a3b2c9d7e1fa3b4c5d6e7f89034"]
}
```

DSP bidder 热路径只使用成功发布的配置快照。MVP 采用 `at=1` first-price CPM；频控、pacing、Deal、video/native 和 user-level targeting 不在首期支持范围。

## 4. DMP API

DMP 在线撤回使用独立 Valkey ACL/keyspace 的共享 deny gate，ClickHouse 保存权威 tombstone。受众解析通过原子 admission 登记 in-flight lease，返回前复核 deny 并释放 lease；gate 不可用或 epoch 未恢复时返回 `503 policy_state_unavailable`，不得返回受众。SSP 也使用跨实例 subject admission lease，覆盖受众解析和 DSP fan-out。撤回原子阻止两侧新 admission，持久化撤回状态并等待 SSP auction 与 DMP lookup 全部 drain 后才返回 `completed`；迟到结果必须丢弃，pending 期间不得发送该身份或使用其受众结果。

### 4.1 内部实时/批量接口

| Method / path | 调用方 | 请求/响应 | 语义 |
|---|---|---|---|
| `POST /internal/v1/audience:resolve` | SSP | namespace、伪名 token、tenant、purpose/consent、max segments → snapshot/provider/segments | 只读已发布快照；无匹配返回空集合；拒绝授权不返回受众；deny gate 不可用返回 `503 policy_state_unavailable` |
| `POST /internal/v1/identity:revoke` | SSP | namespace、`subject_tokens[]`（当前所有生效及双读窗口内 key version 的 token，每项含 `key_version`）、revocation ID、effective time | 原子拒绝新 admission，持久化 tombstone 并 drain 旧 in-flight lease；返回 `202 pending` 或 `completed`；completed 表示 gate、tombstone、状态和 drain 均已确认 |
| `GET /internal/v1/identity-revocations/{revocation_id}` | SSP | revocation ID → online status、gate epoch、drained lookup count、physical deletion status | 用于恢复异步撤回状态；`completed` 表示 admission 已关闭且旧 lookup 已 drain，不代表历史快照物理删除完成 |
| `POST /internal/v1/events:batch` | SSP/授权事件生产方 | `batch_id`、schema version、events[] → accepted/rejected counts | 有界批量写 ClickHouse；至少一次投递，不承诺写入时唯一去重；分析按 event ID 去重 |
| `POST /internal/v1/snapshots` | 数据导入控制器 | provider、namespace、schema、source version、预期行数/checksum → snapshot/job ID | 创建不可变导入版本 |
| `POST /internal/v1/snapshots/{id}/members:batch` | 数据导入控制器 | batch ID、membership rows[]（`subject_token` 必须由 SSP 的 `subject-tokens:derive` 派生，DMP 不接收原始标识）→ accepted count | 批量导入，幂等重放；不直接对在线读取可见 |
| `POST /internal/v1/snapshots/{id}:publish` | DMP 管理/导入控制器 | expected rows/checksum → published snapshot ID | 校验完成后追加 publication marker |
| `GET /internal/v1/snapshots/{id}` | DMP 管理/导入控制器 | job status、counts、checksum、published_at | 查询导入状态 |

`audience:resolve` 请求核心字段：`request_id`、`tenant_id`、`provider_id`（可选）、`namespace`、`subject_token`、`consent_context`、`purposes`、`max_segments`。成功响应只含 `request_id`、`snapshot_id`、`provider_id`、`segments: [{id}]`、`resolved_at`；不能暴露 ClickHouse 行结构或用户画像详情。

### 4.2 DMP 管理 API

| Method / path | 用途 | 主要数据 |
|---|---|---|
| `GET/POST /admin/v1/providers`、`PUT /admin/v1/providers/{id}` | 数据提供方和 namespace 管理 | provider ID、namespace、许可/状态版本 |
| `GET/POST /admin/v1/segments`、`PUT /admin/v1/segments/{id}` | Segment 定义版本 | segment ID/name、provider、状态、valid_from |
| `GET /admin/v1/snapshots` | 快照管理/审计 | snapshot ID、状态、行数、校验和、发布时间 |
| `GET /healthz` / `GET /readyz` | 运维探针 | API 存活、ClickHouse 可用和当前发布快照版本 |

DMP 管理变更写入不可变版本；不把 ClickHouse 当作任意行级事务数据库。删除/撤回要求通过新版本、失效标记及法规批准的清理流程执行。

## 5. 安全与兼容规则

- CORS：`/sdk/v1/ad-requests`、`/sdk/v1/identity*` 精确列举 sandbox Portal origin；`/sdk/v1/impressions` 与 `/sdk/v1/events` 由无 `allow-same-origin` 的沙箱 iframe（Origin 为 `null`）调用，不使用 cookie/credentials，以 event token 作为唯一鉴权，响应 `Access-Control-Allow-Origin: *` 且不带 `Allow-Credentials`，token 校验失败一律 4xx。所有 SDK API 做 payload 上限、placement allowlist、rate limit 和 abuse protection。
- render URL 为短期签名地址，host allowlist；iframe sandbox/CSP 阻止跨域创意访问 SSP cookie/storage。诊断信息默认脱敏。
- 生产 supply 的授权和透明度需同时维护 ads.txt/app-ads.txt 授权关系、sellers.json seller 映射与 OpenRTB `source.schain`；其校验来源、刷新周期、无效/缺失时策略和责任方由供给接入 profile 明确。仅 sandbox 可使用固定测试 fixture。
- 浏览器 impression/click 上报仅作测量/调试输入，不直接产生财务分录。计费由 SSP/DSP 受信服务端策略和账本事务确认。
- OpenRTB 2.6 API 契约由 SSP 与 DSP 双边 profile 确定支持字段和宏；自有 SDK/Admin API 变更通过路径主版本和契约测试管理。
