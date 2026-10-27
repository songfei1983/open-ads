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

常见状态：`202` 异步操作已接受但尚未完成；`400` 请求无效；`404` 管理资源不存在；`409` 版本冲突或非法状态迁移；`413` 超过请求体上限；`422` 业务规则不满足；`429` 限流；`503` 依赖不可用。实时 bidder 超时按 no-bid 处理，不等待管理面错误细节。

自有 JSON API 的错误 `code` 使用稳定的机器可读目录，不随 message 本地化而改变：

| HTTP | `code` | 用途/处置 |
|---|---|---|
| 400 | `invalid_request` | JSON、必填字段、标识符或 header 格式错误 |
| 401 | `unauthenticated` | 缺少或无效的服务/用户凭据 |
| 403 | `forbidden` | 身份有效但没有资源/tenant/action 权限 |
| 404 | `not_found` | 资源不存在或不属于调用方可见范围 |
| 409 | `version_conflict` | 乐观锁版本不匹配 |
| 409 | `idempotency_key_reused` | 同一幂等键被不同请求体重用 |
| 409 | `invalid_state_transition` | 当前状态不允许该操作 |
| 422 | `business_rule_violation` | 业务校验失败；可返回安全的 field errors |
| 429 | `rate_limited` | 按 `Retry-After` 重试 |
| 503 | `dependency_unavailable` | 依赖不可用或安全状态未就绪 |

### 1.2 认证、授权与审计

| 调用面 | 身份与认证 | 授权边界 |
|---|---|---|
| Supply → SSP OpenRTB | 每个 supply endpoint 独立 mTLS client identity；网关校验客户端证书，应用从已登记证书指纹映射 supply endpoint、tenant、publisher/account 和默认 placement policy | OpenRTB body 中的 `seller`、`site.publisher.id` 等仅是声明值，必须与 profile 一致；未知/停用 endpoint 拒绝，不从未认证字段选租户 |
| SSP → DSP、SSP → DMP | mTLS workload identity，服务端按已登记 service/partner + tenant allowlist 授权；DMP API 不接受匿名内部流量 | 每个请求限定调用服务、tenant、namespace/purpose、目标 DSP endpoint；不接受客户端自报 partner/tenant 覆盖认证映射 |
| DMP membership tokenization adapter | 专用 mTLS service identity；原始 provider pseudonym 只发送到 SSP identity service，DMP 只接已 tokenized membership | 必须具备合同授权、namespace 映射和最小化导入权限；无法映射 SSP subject 的记录拒绝，HMAC key 不交付给 provider、adapter 或 DMP |
| DSP → SSP creative registry sync | DSP mTLS identity + DSP endpoint/tenant 绑定；请求携带幂等 `Idempotency-Key` 和单调版本 | 只能更新该 DSP 已绑定 endpoint 的 creative registry，不得指定任意 endpoint/tenant |
| SSP → DSP `nurl`/`lurl`/`burl` | DSP callback 使用已注册 HTTPS host；callback 按 profile 使用 HMAC 请求签名或 mTLS 认证；reservation reference 使用 AEAD 并绑定 DSP、tenant、事件和期限 | 只允许 profile 中登记目标，禁止跟随未验证 redirect；DSP 校验调用方身份、reservation、终态、金额/币种和事件键 |
| 管理 API | OIDC/OAuth2 用户 token；服务端校验 issuer/audience，MFA 由 IdP 策略要求；服务到服务管理操作仍使用 workload identity | RBAC 角色至少区分 `read-only`、`campaign-operator`、`finance-operator`、`privacy-operator`、`tenant-admin`；按 tenant/对象授权。涉及预算/ledger 调整、publish、creative、revoke 的操作写不可变审计记录（actor、action、对象、前后版本、request ID、时间、原因），禁止记录密钥/原始身份 |
| 浏览器 SDK/renderer | 公共 SDK 请求通过 HTTPS、Origin allowlist、placement 配额和短期 AEAD token 限制，不把 CORS 当作身份认证；SDK 不持有管理凭据 | impression token 只授权绑定的一次 tracker event。opaque iframe 的 `Origin: null` 只允许在 token-authenticated impression endpoint 且只接受 safelisted `text/plain` 请求时使用；不回 `Access-Control-Allow-Origin: *`，不接收 cookie/ambient credentials |
| `identity:revoke` | 原 subject 持有 SSP 签发的短期 subject capability；publisher 由已认证 publisher/supply 管理凭据调用并提供绑定 subject 的授权证明。不可仅凭 `subject_id` 或 `reason` 撤回/查询 | revoke 只允许撤销 caller 可证明控制的 subject；查询仅限原 capability 或授权 publisher。SSP 再用自身 mTLS 身份调用 DMP，DMP 按 tenant/namespace 校验 |

凭据采用独立环境/轮换周期，密钥由部署密钥管理设施注入；不得在 URL、普通日志、错误响应或 browser bundle 中暴露。所有内部 HTTP 明确设置请求大小、超时、每租户限流和审计关联 ID。

`Idempotency-Key` 用于会创建/触发不可逆操作的自有 JSON API（管理资源 POST、creative upload、publish、snapshot import/publish、批量事件写入、identity revoke）；OpenRTB auction、notice 与 impression 使用各自的标准/领域稳定 ID，不改用此 header。键作用域为 `(authenticated tenant, method, normalized route, key)`，同 key + 同规范化 body hash 在至少 7 天保留期内重放原 HTTP status/body；同 key + 不同 body 返回 `409 idempotency_key_reused`。超过保留期不得承诺去重，异步任务仍由资源/事件 ID 幂等。

### 1.2.1 不透明 token 的保密与认证

`render_token`、`click_token`、`event_token` 与 `reservation_ref` 均为 bearer credential，使用 AEAD 加密并认证；不得采用可由客户端解码读取 claims 的明文签名 token。首期格式固定为 AES-256-GCM、96-bit CSPRNG nonce，二进制 envelope 以无 padding 的 base64url 编码，且包含格式/schema version、key version、nonce、ciphertext 和认证标签。AAD 至少绑定产品域、token 类型、schema version 与接收服务 audience，防止 token 跨用途或跨服务重放。Claims 仅含接收端所需的资源/事件绑定、expiry、一次性标识；event token 可额外包含财务恢复所需金额、币种、reservation reference 和已登记 `burl` 目标；不得包含用户标识、原始 consent 或其他非必要身份数据。

服务端须先认证解封，再校验 audience、tenant/auction/bid/imp/placement 绑定、有效期和一次性/幂等状态。无效 tag、未知 key version 或绑定不匹配一律 fail closed。所有 token 及解封后的敏感 claims 均须从 access log、trace、错误、指标标签和普通应用日志中剔除；路径中的 render/click token 需在代理和应用日志中脱敏。密钥按 token 用途分离并可轮换；旧 key 至少保留至其最后签发 token 的最长 TTL、关联 reservation settlement grace 与 notice retry window 均结束。AEAD 不替代 TLS、调用方身份认证、授权、一次性约束或账务幂等。

## 2. SSP API

SSP 对外提供 OpenRTB exchange endpoint、浏览器 Client API 和受保护管理 API；对 DSP/DMP 的调用由 SSP 发起。RTB handler 读预热内存/Valkey 缓存，不直接读 `ssp_db`。

### 2.1 Supply OpenRTB endpoint

| Method / path | 调用方 | 请求/响应 | 语义 |
|---|---|---|---|
| `POST /openrtb2/auction` | 经认证 Supply endpoint | OpenRTB 2.6 `BidRequest` → `BidResponse`/no-bid | 从 client identity 映射 supply profile；校验声明 publisher/site 与 profile；以 `tmax` 限定总延迟 |

Supply 直连首期 profile 返回 `adm` 中仅含 SSP `/render/v1/{render_token}` iframe URL 与受控 click URL 的 HTML markup；SSP 生成自身的 signed `nurl`/`lurl` callback 供 Supply 回报 upstream win/loss。Supply 的 impression callback 通过下列 SSP endpoint 送达，经验证后才触发 publisher payable 和下游 DSP `burl`；Supply 若不支持 callback，则该路径只提供非账务预览，不能声明 billable。入口不把 Client SDK DTO 当作 OpenRTB 请求。

### 2.2 Browser Client API

| Method / path | 调用方 | 请求/响应 | 语义 |
|---|---|---|---|
| `POST /sdk/v1/ad-requests` | Portal/JS SDK | placement、slot、去敏 page context、consent fixture → SDK AdResponse/no-fill | SSP 内部创建 auction 并映射到 OpenRTB；`Cache-Control: no-store` |
| `POST /sdk/v1/identity` | JS SDK/Publisher page | 可选 publisher pseudonymous subject → SSP-scoped subject ID、短期 subject capability、expiry、status | 明确 opt-in；ID 按 publisher scope 隔离并存 first-party storage；capability 仅可撤回/查询该 subject，不写日志；无 consent 返回 `identity: null` |
| `POST /sdk/v1/identity-sync` | JS SDK/Publisher page | subject ID、partner ID → allowlisted one-time sync URL/state | 可选 legacy/browser match；只允许登记 partner 和固定 return URL |
| `POST /sdk/v1/identity:revoke` | 已授权 subject/Publisher privacy flow | `Authorization: Bearer <subject capability>`、reason、request ID → `pending`/`completed` 与 revocation ID | 必须认证并证明 subject 控制权；SSP 先阻止新 identity-bearing auction；仅在 SSP 活跃 auction 与 DMP 所有有效 key version 的 in-flight lookup 均 drain、撤回状态持久化后报告 completed；pending 期间不发送身份/受众 |
| `GET /sdk/v1/identity-revocations/{revocation_id}` | SDK/Publisher privacy flow | revocation ID → 当前 `pending`/`completed` 状态 | 只允许原 subject/授权 publisher 查询，不返回 tombstone 或其他用户数据 |
| `POST /sdk/v1/events` | JS SDK | client event ID、event type、token、发生时间 → 接收状态 | SDK page origin 必须精确 allowlist；仅作为客户端测量/调试信号；不得单独触发财务 ledger |
| `POST /sdk/v1/impressions` | SSP 托管的 sandbox render iframe | event ID、token、发生时间 → accepted/duplicate/rejected | token-only、无 cookie/credentials；接受 sandbox iframe 的 `Origin: null` 但不启用通用 CORS；校验 auction/bid/imp/placement 绑定、expiry 和一次性约束；仅 sandbox profile 可触发 payable ledger，不构成生产 IVT 证明 |
| `GET /click/v1/{click_token}` | Browser | opaque token → 记录 click 并 302 到竞价记录中的 allowlisted landing URL | 不接受客户端 redirect 参数；click 不计费；重复请求去重 |
| `GET /render/v1/{render_token}` | Browser iframe | opaque token → SSP 托管 HTML 壳 | nonce CSP 仅开放 SSP 自有脚本、图片与 impression 连接；no-store/no-referrer；iframe 使用 `sandbox="allow-scripts"` 且不授予 `allow-same-origin` |
| `GET /render/v1/assets/{render_token}` | HTML 壳中的 image element | opaque token → PNG/JPEG/WebP 字节 | 独立图片字节端点；严格 MIME/hash 校验，`default-src 'none'` CSP；no-store/no-referrer |

`ad-requests` 示例：

```json
{
  "request_id": "018f7a3b2c9d7e1fa3b4c5d6e7f89015",
  "placement_id": "018f7a3b2c9d7e1fa3b4c5d6e7f89012",
  "slot": { "id": "leaderboard", "width": 728, "height": 90 },
  "page": { "origin": "https://publisher.test", "path": "/demo" },
  "consent": { "source": "test-fixture", "value": null }
}
```

filled response：

```json
{
  "request_id": "018f7a3b2c9d7e1fa3b4c5d6e7f89015",
  "status": "filled",
  "ad": {
    "creative_id": "018f7a3b2c9d7e1fa3b4c5d6e7f89013",
    "format": "image",
    "render_url": "https://render.test.example/render/v1/signed/short-lived-token",
    "click_url": "https://ssp.test.example/click/v1/opaque-token",
    "event_token": "short-lived-opaque-token",
    "expires_at": "2026-10-01T12:00:30Z"
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

响应只返回 SSP 生成的 opaque `subject_id`、受限 scope 的短期 `subject_capability`、`expires_at`，不回显 publisher ID。capability 只存哈希并通过 TLS 返回，不放进 URL/普通日志；没有 publisher subject 时，可由 SSP 生成随机 subject ID 并在 consent 允许时由 SDK 保存于 publisher first-party storage。

no-fill 使用 `200` + `{"request_id":"...","status":"no_fill"}`。错误响应包含 `code`、`request_id` 和对客户端安全的简短 `message`。无广告不是 transport error。测试 diagnostics 默认不返回 bidder、用户或完整 OpenRTB 详情。

首期 renderer 只输出 SSP 托管的静态 PNG/JPEG/WebP image creative，不执行 DSP HTML/JavaScript。Impression 请求只携带 AEAD event token、稳定 `event_id` 和 `occurred_at`；SSP 从经认证解封的 token 恢复金额、currency、DSP、reservation 和 `burl` 目标，不依赖 Valkey 或 best-effort 竞价记录。Sandbox profile 中第一个有效 impression 在同一 `ssp_db` 事务写入唯一 billable delivery event、publisher payable ledger 和 DSP billable outbox；之后由 outbox 按 DSP `burl` 的 sandbox JSON POST profile 重试。Publisher payable 暂为该有效 DSP billable amount 的 100%，不代表生产 payout。普通 `/sdk/v1/events` 与 click 永不触发财务分录。

### 2.3 Portal 与 SSP 管理 API

| Method / path | 调用方 | 主要请求/响应 | 语义（补充） |
|---|---|---|---|
| `GET /admin/v1/publishers` / `POST /admin/v1/publishers` | SSP 管理台 / 管理脚本 | Publisher 配置 DTO；响应包含 ID/version | — |
| `GET /admin/v1/properties` / `POST /admin/v1/properties` | 同上 | Property 配置与 Publisher 外部 ID | — |
| `GET /admin/v1/placements` / `POST /admin/v1/placements` | 同上 | `auction_policy_id`、格式、floor、币种、隐私/计费策略版本 | placement 关联已发布的 AuctionPolicy |
| `GET /admin/v1/auction-policies` / `POST /admin/v1/auction-policies` / `PUT /admin/v1/auction-policies/{id}` | 同上 | `at`、`tie_breaker`、`floor_enforcement`、`currency_policy`；响应包含 ID/version | 策略发布后由 placement 引用；MVP 仅支持 `at=1`、已定义的 tie-break 与 exact-match 币种策略 |
| `POST /admin/v1/auction-policies/{id}:publish`、`POST /admin/v1/placements/{id}:publish` | SSP 管理台 | 版本、变更原因 → 发布状态/config version | 默认 draft；publish 校验关联资源、floor/currency/auction policy 后产生不可变快照；未 publish 的变更不进入热路径 |
| `GET /admin/v1/supply-endpoints` / `PUT /admin/v1/supply-endpoints/{id}` | 同上 | Supply 协议 profile、ads.txt/app-ads.txt seller/account 映射、schain 策略和状态 | — |
| `GET /admin/v1/dsp-endpoints` / `POST /admin/v1/dsp-endpoints` / `PUT /admin/v1/dsp-endpoints/{id}` | 同上 | `dsp_endpoint_id`、bid URL、OpenRTB profile、`callback_host_allowlist`、`priority`、`timeout_ms`、status、version | `callback_host_allowlist` 用于 SSRF 防护；`priority` 越小排序越优先，仅作为同价 tie-break |
| `GET /admin/v1/dsp-endpoints/{id}/creative-whitelist` | 同上 | `whitelist: [{creative_id, asset_ref, asset_sha256, width_px, height_px, mime_type, landing_url_host, status, version}]` | 只读查询；白名单唯一写入路径为 DSP → SSP 内部同步 API；`asset_sha256` 为 64 位小写 hex |
| `GET /admin/v1/config-outbox` | 同上 | `?status=pending|failed` + limit/cursor → 未发布 / 发布失败的 outbox 事件列表 | 管理台 UI 展示"配置发布进度与失败项"，配合 retry 手动重放 |
| `POST /admin/v1/config-outbox/{event_id}:retry` | 同上 | event_id → `{status: accepted}` | 对 failed_attempts >= 5 的 outbox 手动重放，重置失败计数 |
| `POST /internal/v1/identity-links:complete` | DSP identity-sync callback | sync token、DSP-specific user ID、consent receipt/version、expiry → link status/version | 将 DSP ID 映射为当前 SSP subject 的 pairwise `buyeruid`；不得接受任意 user ID upsert；sync token 生命周期绑定 partner+subject+consent，仅可消费一次 |
| `DELETE /admin/v1/identity-links/{partner_id}/{subject_id}` | SSP 管理台隐私管理 tab | deletion reason/consent version → revocation status | 撤回后停止下发，并按保留策略清理/失效映射及缓存 |
| `POST /internal/v1/dsp-endpoints/{dsp_endpoint_id}/creative-registry:upsert` | DSP → SSP（creative 白名单自动同步） | `creatives: [{creative_id, asset_ref, asset_sha256, width_px, height_px, mime_type, landing_url_host, status, version}]` → `{accepted_count, rejected: [{creative_id, reason}]}` | 路径相对 SSP host；DSP 管理台批量上报；tenant/endpoint 从 mTLS identity 绑定，不信任 body；SSP 按 endpoint + creative_id 幂等更新 |
| `GET /admin/v1/runtime/instances` | SSP 运维/管理台 | 实例、已加载版本、最后心跳、ready 状态 | 观察配置扇出；返回按 tenant/resource group 聚合的版本/陈旧度，不返回机密 |
| `POST /internal/v1/runtime/config-status` | SSP 实例 → SSP 控制面 | instance incarnation、manifest version、各资源快照版本、报告时间 | mTLS 实例身份；持续上报，状态有 TTL；超时实例视作未就绪并告警 |
| `GET /admin/v1/billable-outbox` | SSP finance-operator | pending/quarantined 状态、分页 | 查看 DSP burl 投递和隔离项；目标 URL 脱敏，仅 finance-operator 可查看必要 host |
| `POST /admin/v1/billable-outbox/{event_id}:resolve` | SSP finance-operator | accept/reject + reason + version | 对 4xx、grace timeout、内容冲突的账务 outbox 人工隔离处置；拒绝后需追加 payable reversal 或显式标记 sandbox discrepancy |
| `GET /healthz` / `GET /readyz` | 运维探针 / 负载均衡 | 存活、配置快照就绪版本；不返回密钥或业务数据 | readyz 失败时摘除该实例的 RTB 流量（SSP bid handler、browser SDK handler、internal identity）；管理 API 与 refresh worker 即使 readyz 失败仍继续处理配置发布。 |

SSP 管理变更写 `ssp_db` 与配置 outbox，由异步刷新器（见 LLD §2.4.1）更新 Valkey/进程快照；管理 API 不直接改缓存作为唯一写入。

### 2.4 SSP 发起的下游 API

| Method / path | 接收方 | 协议 | 主要内容 |
|---|---|---|---|
| `POST {dsp.bid_url}` | DSP | OpenRTB 2.6 | 标准 BidRequest → BidResponse/no-bid；deadline 不超过 SSP 剩余 auction budget |
| `POST /internal/v1/audience:resolve` | DMP | JSON | pseudonymous token + consent/purpose → provider/segment IDs |
| `POST /internal/v1/identity:revoke` | DMP | JSON | 所有有效 subject-token key versions + consent/purpose → tombstone、全版本 gate 和 drain status |
| `GET /internal/v1/identity-revocations/{revocation_id}` | DMP | JSON | 查询该撤回的在线 deny、各 key version 覆盖、drain 和物理删除状态 |
| `POST /internal/v1/events:batch` | DMP | JSON | 去敏事件批次；at-least-once、稳定 event ID |

DSP 返回的 `nurl`/`lurl`/`burl` 只允许命中该 DSP 注册并验证过的 callback host/egress policy（由 §2.3 DSP endpoint 的 `callback_host_allowlist` 字段配置），防止 SSP 被利用为 SSRF proxy。

#### 2.4.1 Supply 回调

Supply 直连使用 SSP 对 Supply 的响应通知 URL：胜出后的 `nurl`/`lurl` 是 SSP host 上的一次性、签名 callback，Supply 只能回报该 request 的 win/loss，不提供账务金额。Impression 必须通过以下 endpoint 回传并由 SSP 解封 Supply-audience AEAD token 还原计费上下文；此 token 与 SDK tracker token 使用不同 audience，但绑定同一 event key 并共用账务幂等事务。Supply 不支持回调时该流量只可作为非账务预览。

| Method / path | 调用方 | 请求/响应 | 语义 |
|---|---|---|---|
| `POST /internal/v1/supply/impressions` | 已认证 Supply endpoint | Supply-audience AEAD event token、event ID、occurred_at → accepted/duplicate/rejected | mTLS Supply identity 绑定 tenant；token 绑定 supply/auction/bid/imp 且一次性；与 SDK tracker 共用 event key、落账与 outbox 事务，但使用不同 audience 的 token |

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
| 按 bidder 返回的 `lurl` 调用（loss notice） | SSP/exchange | GET 或 POST；loss code macro `${AUCTION_LOSS}` 已替换 | 记录 loss 状态；在同一事务中释放 reservation 的 held 额度 → 退回全局预算池（leased_total 下降、available 上升）；仅在确认不再 billable 后释放 |
| 按 bidder 返回的 `burl` 调用（billable notice） | SSP/exchange | MVP：HTTPS POST JSON（body 见下方示例）；reservation_ref 与 source_event_key 在 body 中传递 | 校验 event/auction/bid/imp、reservation、币种/金额上限后，在事务中写 advertiser spend ledger 并更新所有适用预算范围的 spent_micros（reservation held 转为 spent_micros，不重复加回全局池）；重复 event 幂等成功 |
| `GET /healthz` / `GET /readyz` | 运维探针 | 无业务 body | 报告服务存活、配置版本/预算租约可用状态 |

通知 URL 由 DSP 在 BidResponse 对应 bid 的 `nurl`、`lurl`、`burl` 提供。OpenRTB 不定义通用 JSON notice payload；双方 MVP profile 的方法、宏、超时、重试、成功响应及 reservation/source event ID 按 LLD §2.2 与 §3.1.2 的幂等规则执行。billing price 宏的替换和结算金额按上述 profile 确定，不以 win price 自动替代 billable price。SSP 仅调用已登记 bidder 的合法 HTTPS host（由 §2.3 DSP endpoint 的 callback_host_allowlist 约束），并校验重定向/解析后的目标，避免 SSRF。

MVP `burl` JSON body：

```json
{
  "event_id": "018f7a3b2c9d7e1fa3b4c5d6e7f89016",
  "source_event_key": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "auction_id": "018f7a3b2c9d7e1fa3b4c5d6e7f89017",
  "bid_id": "018f7a3b2c9d7e1fa3b4c5d6e7f89018",
  "imp_id": "018f7a3b2c9d7e1fa3b4c5d6e7f89019",
  "reservation_ref": "opaque-aead-reservation-reference",
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
| `POST /admin/v1/campaigns/{id}:publish` | 发布 campaign 聚合及其引用版本 | 校验状态/时间窗/引用，返回 `config_version`、发布状态 |
| `POST /admin/v1/line-items/{id}:publish` | 发布 line item、targeting、budget 与 creative 关联 | 校验预算/币种/targeting/creative 后生成不可变快照版本 |
| `POST /admin/v1/creatives/{id}:publish` | 发布 creative 版本 | 校验 asset hash/MIME/尺寸/landing URL 后生成不可变版本 |
| `GET /admin/v1/budgets/{line_item_id}` | 查看预算账本汇总 | spent/reserved/available、currency、ledger version |
| `GET /admin/v1/runtime/instances` | 查看 DSP 实例 | 每实例加载版本、最后心跳、ready 状态及预算 lease 健康度 |
| `POST /internal/v1/runtime/config-status` | DSP 实例上报 | mTLS instance identity；报告 manifest/config version、时间和 readiness，状态过期视为未就绪 |
| `GET /admin/v1/billable-quarantine` | finance-operator 查询 | DSP 收到的 late/conflicting/invalid notice 及 payload hash、reason、reservation 绑定摘要 |
| `POST /admin/v1/billable-quarantine/{id}:resolve` | finance-operator 处置 | accept/reject、原因、关联分录；迟到 settlement 仅在原预算 scope/currency 验证后提交，账户超限时暂停新 bid 并显式告警 |

管理 API 不允许客户端直接调整 `spent` 汇总字段。财务调整必须生成带原因的新 ledger entry，并记录关联的审计事件。

首期 `line-items` 写入 DTO 至少包含 `campaign_id`、状态、`bid_cpm_micros`、currency、daily/lifetime budget、可选起止时间、`targeting` 和关联 creative IDs。line item 可继承 campaign 起止时间，或定义更窄窗口；campaign 自身也有必填起点/可选终点。daily budget 按 UTC 自然日聚合，`period_start` 为 UTC 日期，`00:00 UTC` 开始新 scope；lifetime `period_start` 固定为有效 LineItem 起点的 UTC 日期。daily 账户在新 UTC 日期首次租额申请时由 allocator 以唯一键惰性创建。首期租约参数、续租阈值及超限预算下调行为见 README §7.1。Targeting 仅支持 `site_domains`、`page_path_prefixes`、`banner_sizes`；同数组内 OR，指定维度间 AND。domain 为规范化 host 精确匹配，DSP 从完整 `site.page` URL 解析 path，path prefix 按路径段边界匹配，size 精确匹配；省略维度表示不限。未知字段或不支持条件在 publish 时返回 `422`，成功发布生成不可变 targeting version。首期 creative 只接受受控 asset store 的静态 PNG/JPEG/WebP 与 HTTPS landing URL，不接受任意 HTML/JS。

Asset store 是无领域模型的共享对象存储基础设施：DSP 控制面只写、SSP renderer 只读。对象不可覆盖，`asset_ref` 是不透明 key；上传时校验实际文件签名与解码尺寸。创建 creative 时服务端从 asset metadata 复制宽高与 hash 到 creative 记录，不接受调用者自报尺寸；publish 时复核 asset hash、MIME 和尺寸。OpenRTB sandbox profile 在 `seatbid[].bid[].ext.openads` 中携带 `asset_ref`、`asset_sha256`、`asset_width_px`、`asset_height_px` 和 `landing_url`；SSP 校验其与注册 creative 的 metadata 一致后绑定到短时 render token，不执行 `adm` 中的任意 HTML/JavaScript。Renderer 通过服务端只读凭据取图，不将对象存储凭据暴露给浏览器。

```json
{
  "campaign_id": "018f7a3b2c9d7e1fa3b4c5d6e7f89014",
  "status": "active",
  "bid_cpm_micros": 2500000,
  "currency": "USD",
  "daily_budget_micros": 1000000000,
  "lifetime_budget_micros": 10000000000,
  "targeting": {
    "site_domains": ["publisher.test"],
    "page_path_prefixes": ["/sports"],
    "banner_sizes": [{"w": 300, "h": 250}]
  },
  "creative_ids": ["018f7a3b2c9d7e1fa3b4c5d6e7f89013"]
}
```

DSP bidder 热路径只使用成功发布的配置快照。MVP 采用 `at=1` first-price CPM；频控、pacing、Deal、video/native 和 user-level targeting 不在首期支持范围。

## 4. DMP API

DMP 在线撤回使用独立 Valkey ACL/keyspace 的共享 deny gate，ClickHouse 保存权威 tombstone。受众解析通过原子 admission 登记 in-flight lease，返回前复核 deny 并释放 lease；gate 不可用或 epoch 未恢复时返回 `503 policy_state_unavailable`，不得返回受众。SSP 也使用跨实例 subject admission lease，覆盖受众解析和 DSP fan-out。撤回原子阻止两侧新 admission，持久化撤回状态并等待 SSP auction 与 DMP lookup 全部 drain 后才返回 `completed`；迟到结果必须丢弃，pending 期间不得发送该身份或使用其受众结果。

### 4.1 内部实时/批量接口

| Method / path | 调用方 | 请求/响应 | 语义 |
|---|---|---|---|
| `POST /internal/v1/audience:resolve` | SSP | namespace、伪名 token、tenant、purpose/consent、max segments → snapshot/provider/segments | 只读已发布快照；无匹配返回空集合；拒绝授权不返回受众；deny gate 不可用返回 `503 policy_state_unavailable` |
| `POST /internal/v1/identity:revoke` | SSP | namespace、`subject_tokens: [{key_version, token}]`、revocation ID、effective time | 原子拒绝所有版本 admission，持久化每个 token-version tombstone 并 drain 旧 in-flight lease；返回 `202 pending` 或 `completed`；completed 要求所有有效 key version 已 deny、tombstone 已持久、gate epoch 对齐且 drain 完成 |
| `GET /internal/v1/identity-revocations/{revocation_id}` | SSP | revocation ID → online status、gate epoch、drained lookup count、physical deletion status | 用于恢复异步撤回状态；`completed` 表示 admission 已关闭且旧 lookup 已 drain，不代表历史快照物理删除完成 |
| `POST /internal/v1/events:batch` | SSP/授权事件生产方 | `batch_id`、schema version、events[] → accepted/rejected counts | 有界批量写 ClickHouse；至少一次投递，不承诺写入时唯一去重；分析按 event ID 去重 |
| `POST /internal/v1/snapshots` | 数据导入控制器 | provider、namespace、schema、source version、预期行数/checksum → snapshot/job ID | 创建不可变导入版本 |
| `POST /internal/v1/snapshots/{id}/members:batch` | 数据导入控制器 | batch ID、membership rows[] → accepted count | 批量导入，幂等重放；不直接对在线读取可见 |
| `POST /internal/v1/snapshots/{id}:publish` | DMP 管理/导入控制器 | expected rows/checksum → published snapshot ID | 校验完成后追加 publication marker |
| `GET /internal/v1/snapshots/{id}` | DMP 管理/导入控制器 | job status、counts、checksum、published_at | 查询导入状态 |

`audience:resolve` 请求核心字段：`request_id`、`tenant_id`、`provider_id`（可选）、`namespace`、`subject_token`、`consent_context`、`purposes`、`max_segments`。成功响应只含 `request_id`、`snapshot_id`、`provider_id`、`segments: [{id}]`、`resolved_at`；不能暴露 ClickHouse 行结构或用户画像详情。

Membership 导入只接收已由受信身份 tokenization adapter 转换的 `{subject_token, key_version, segment_ids}`，拒绝原始 email/phone/device ID。Adapter 由 SSP identity service 的受控密钥環境接收具備合同授權的 provider namespace pseudonym，先经已授权 identity mapping 绑定到 SSP subject，再为每个仍被快照引用的 key version 生成 HMAC token；HMAC key 永不交给 DMP 或 provider。若没有可证明的稳定身份映射，该记录不得进入可在线解析的 snapshot。密钥轮换期间的新旧版本均可被解析/撤回，旧版快照在撤回完成并完成物理清理前不得退役对应 key version。

### 4.2 DMP 管理 API

管理 API 使用 bearer token，且 token 在服务端绑定单一 tenant；请求体不接受 `tenant_id`，出现未声明字段即拒绝。

| Method / path | 用途 | 主要数据 |
|---|---|---|
| `GET/POST /admin/v1/namespaces`、`PUT /admin/v1/namespaces/{id}` | Identity namespace 定义版本 | namespace ID、显示名、hashing algorithm（仅 `hmac-sha256`）、subject key version、保留月数（≤ 3）、状态 |
| `GET/POST /admin/v1/providers`、`PUT /admin/v1/providers/{id}` | 数据提供方定义版本 | provider ID、显示名、所属 namespace、许可版本、状态 |
| `GET/POST /admin/v1/segments`、`PUT /admin/v1/segments/{id}` | Segment 定义版本 | segment ID/name、provider、描述、状态、valid_from |
| `GET /admin/v1/audit` | 控制面审计查询 | `resource_type`、`resource_id`、`limit`（≤ 200）→ actor、action、版本、request_id、outcome |
| `GET /admin/v1/snapshots` | 快照管理/审计 | snapshot ID、状态、行数、校验和、发布时间 |
| `GET /healthz` / `GET /readyz` | 运维探针 | API 存活、ClickHouse 可用和当前发布快照版本 |

DMP 管理变更写入不可变版本；不把 ClickHouse 当作任意行级事务数据库。每个 `PUT` 必须提交当前 `version`，不匹配返回 `409 version_conflict`，重复创建返回 `409 resource_conflict`；字段或引用无效返回 `422 invalid_definition`，`provider.namespace` 与 `segment.provider_id` 变更返回 `422 immutable_field`；未配置 token/store 或存储不可用返回 `503 control_plane_unavailable`。未知路径返回 `404 not_found`，不支持的方法返回 `405 method_not_allowed`（附 `Allow`），两者都使用标准 JSON 错误信封。并发写同一 `version` 时只有一个请求成功，其余返回 `409`；落败版本行保留为审计事实但不影响当前定义。删除/撤回要求通过新版本、失效标记及受控清理流程执行。

每次变更尝试（accepted/conflict/not_found/rejected）都追加一条审计记录，只包含配置的操作者标识、资源标识、版本和 request ID，不记录 bearer token、subject token 或原始身份。

### 4.3 契约工件

本文件是设计阶段的规范基准；实现前必须从每个 JSON endpoint 的 request/response/error 定义生成 OpenAPI 3.1（包含 `Idempotency-Key`、认证 scheme、错误 code、枚举、幂等冲突及保留期），并为 OpenRTB notice、signed token claims 和 browser event 定义 JSON Schema/固定 golden vectors。契约测试必须以这些版本化工件验证 DTO；不得把本文散文解释作为运行时唯一校验依据。

## 5. 安全与兼容规则

- CORS 精确列举 sandbox Portal origin；SDK API 做 payload 上限、placement allowlist、rate limit 和 abuse protection。
- render HTML 壳和图片字节使用不同 endpoint：壳只允许 nonce script、SSP 图片及 tracker 连接，图片 endpoint 的 CSP 为 `default-src 'none'`；两者一致使用 `Cache-Control: no-store`。iframe 仅 `sandbox="allow-scripts"`，不授予 `allow-same-origin`，壳 CSP 的 `frame-ancestors` 只能由该 placement 的授权 publisher origins 生成。Origin `null` 只在 token-only impression callback 接受，不向该 opaque origin 开放通用 CORS。
- render URL 为短期签名地址，host allowlist；iframe sandbox/CSP 阻止跨域创意访问 SSP cookie/storage。诊断信息默认脱敏。
- 生产 supply 的授权和透明度需同时维护 ads.txt/app-ads.txt 授权关系、sellers.json seller 映射与 OpenRTB `source.schain`；其校验来源、刷新周期、无效/缺失时策略和责任方由供给接入 profile 明确。仅 sandbox 可使用固定测试 fixture。
- 浏览器 impression/click 上报仅作测量/调试输入，不直接产生财务分录。计费由 SSP/DSP 受信服务端策略和账本事务确认。
- OpenRTB 2.6 API 契约由 SSP 与 DSP 双边 profile 确定支持字段和宏；自有 SDK/Admin API 变更通过路径主版本和契约测试管理。
