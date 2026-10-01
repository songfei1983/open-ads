# 服务 API 设计

状态：接口设计草案。所有 API 由 SSP、DSP、DMP 各自拥有 DTO、校验器、版本策略和测试；禁止跨服务复用领域类型。路径和字段可在评审时调整，协议边界与认证原则不应被弱化。

## 1. 通用约定

| 项目 | 约定 |
|---|---|
| 网络 | HTTPS；内部服务间使用 mTLS 或等价服务身份；管理 API 不暴露公网 |
| JSON API 版本 | 路径版本，例如 `/sdk/v1`、`/internal/v1`、`/admin/v1`；不兼容变更升主版本 |
| 请求关联 | 接收/返回 `X-Request-ID`；没有时由服务生成；允许 `traceparent` 贯穿调用链 |
| 时间 | RFC 3339 UTC；事件同时保留 `occurred_at` 和服务端 `received_at` |
| 金额 | OpenRTB wire `price` 遵循协议数值字段；领域与数据库使用有符号整数最小单位及 ISO-4217 currency，不用浮点数累计 |
| 幂等 | 账务/事件写入使用稳定 `source_event_key` 或 `Idempotency-Key`；重试复用同一 key |
| 错误 | 自有 JSON API 使用 `{code, message, request_id}`；不回显堆栈、敏感标识或内部 SQL。OpenRTB 接口按 OpenRTB no-bid/HTTP 语义返回，不包通用错误信封 |
| 分页 | 管理 API 使用 `limit` + opaque `cursor`；响应包含 `items`、`next_cursor` |
| 并发更新 | 管理资源返回 `version`/ETag；更新使用 `If-Match`，版本冲突返回 `409` |

常见状态：`400` 请求无效；`401/403` 未认证/无权限；`404` 管理资源不存在；`409` 版本冲突或非法状态迁移；`413` 超过请求体上限；`422` 业务规则不满足；`429` 限流；`503` 依赖不可用。实时 bidder 超时按 no-bid 处理，不等待管理面错误细节。

## 2. SSP API

SSP 对外提供 OpenRTB exchange endpoint、浏览器 Client API 和受保护管理 API；对 DSP/DMP 的调用由 SSP 发起。RTB handler 读预热内存/Valkey 缓存，不直接读 `ssp_db`。

### 2.1 Supply OpenRTB endpoint

| Method / path | 调用方 | 认证 | 请求/响应 | 语义 |
|---|---|---|---|---|
| `POST /openrtb2/auction` | 已注册 Supply | supply credential、mTLS 或签名 | OpenRTB 2.6 `BidRequest` → `BidResponse`/no-bid | 校验版本、request/imp ID、媒体能力与截止时间；以 `tmax` 限定总延迟 |

响应中的竞价结果和通知 URL 遵循 OpenRTB 2.6。入口不把 Client SDK DTO 当作 OpenRTB 请求。

### 2.2 Browser Client API

| Method / path | 调用方 | 认证 | 请求/响应 | 语义 |
|---|---|---|---|---|
| `POST /sdk/v1/ad-requests` | Portal/JS SDK | 短期 scoped test token；未来公开集成按 placement/origin allowlist | placement、slot、去敏 page context、consent fixture → SDK AdResponse/no-fill | SSP 内部创建 auction 并映射到 OpenRTB；`Cache-Control: no-store` |
| `POST /sdk/v1/identity` | JS SDK/Publisher page | placement-scoped test/public token + consent state | 可选 publisher pseudonymous subject → SSP-scoped subject ID、expiry、status | 明确 opt-in；ID 按 publisher scope 隔离并存 first-party storage；无 consent 返回 `identity: null` |
| `POST /sdk/v1/identity-sync` | JS SDK/Publisher page | 同上；已授权的 identity purpose | subject ID、partner ID → allowlisted one-time sync URL/state | 可选 legacy/browser match；只允许登记 partner 和固定 return URL |
| `POST /sdk/v1/identity:revoke` | SDK/Publisher privacy flow | subject-scoped revoke token / publisher authorization | subject ID、reason、request ID → revocation status | SSP 先立即禁止该身份参与竞价/同步并失效缓存；随后可靠传播 DMP tombstone 和 partner unlink |
| `POST /sdk/v1/events` | JS SDK | 与广告 response 绑定的短时 opaque event token | client event ID、event type、token、发生时间 → 接收状态 | 仅作为客户端测量/调试信号；不得单独触发财务 ledger |

`ad-requests` 示例：

```json
{
  "request_id": "client-generated-uuid",
  "placement_id": "test-placement-id",
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
    "creative_id": "creative-test-1",
    "format": "image",
    "render_url": "https://render.test.example/signed/short-lived-token",
    "click_url": "https://ssp.test.example/click/opaque-token",
    "event_token": "short-lived-opaque-token",
    "expires_at": "2026-10-01T12:00:30Z"
  }
}
```

身份 opt-in 请求：

```json
{
  "placement_id": "test-placement-id",
  "publisher_subject": {
    "namespace": "publisher-account-v1",
    "value": "publisher-controlled-pseudonymous-value"
  },
  "consent": { "purpose": "personalized_ads", "receipt_id": "cmp-receipt-ref" }
}
```

响应只返回 SSP 生成的 opaque `subject_id`、`scope`、`expires_at`，不回显 publisher ID。`publisher_subject.value` 仅用于匹配，SSP 只保存带独立 HMAC key 的索引，不保留明文；没有 publisher subject 时，可由 SSP 生成随机 subject ID 并在 consent 允许时由 SDK 保存于 publisher first-party storage。

no-fill 使用 `200` + `{"request_id":"...","status":"no_fill"}`。错误响应包含 `code`、`request_id` 和对客户端安全的简短 `message`。无广告不是 transport error。测试 diagnostics 需单独授权 scope，默认不返回 bidder、用户或完整 OpenRTB 详情。

### 2.3 Portal token 与 SSP 管理 API

| Method / path | 调用方 | 认证 | 主要请求/响应 |
|---|---|---|---|
| `POST /portal/v1/test-tokens` | 已登录的 Portal 用户 | OIDC/session + CSRF 防护 | tenant、placement、Portal origin、scope、TTL → 短期 token、过期时间 |
| `GET /admin/v1/publishers` / `POST /admin/v1/publishers` | SSP 管理员 | 管理员身份、RBAC | Publisher 配置 DTO；响应包含 ID/version |
| `GET /admin/v1/properties` / `POST /admin/v1/properties` | SSP 管理员 | 同上 | Property 配置与 Publisher 外部 ID |
| `GET /admin/v1/placements` / `POST /admin/v1/placements` | SSP 管理员 | 同上 | 格式、floor、币种、隐私/计费策略版本 |
| `GET /admin/v1/supply-endpoints` / `PUT /admin/v1/supply-endpoints/{id}` | SSP 管理员 | 同上 | Supply 协议 profile、凭据 secret reference、状态 |
| `GET /admin/v1/dsp-endpoints` / `POST /admin/v1/dsp-endpoints` / `PUT /admin/v1/dsp-endpoints/{id}` | SSP 管理员 | 同上 | DSP bid URL、支持的 OpenRTB profile、credential secret reference、callback host allowlist、timeout/status |
| `POST /internal/v1/identity-links:complete` | 已登记的 DSP identity-sync callback | DSP service identity + single-use sync token | sync token、DSP-specific user ID、consent receipt/version、expiry → link status/version | 将 DSP ID 映射为当前 SSP subject 的 pairwise `buyeruid`；不得接受任意 user ID upsert |
| `POST /internal/v1/identity:revoke` | SSP | SSP service identity/mTLS | tenant、namespace、subject token、revocation ID、effective time → accepted/status | DMP 追加 revocation tombstone；解析排除该 token，物理清除历史快照按删除 SLA 异步完成 |
| `DELETE /admin/v1/identity-links/{partner_id}/{subject_id}` | SSP privacy/control plane | 管理员或授权隐私请求身份 | deletion reason/consent version → revocation status | 撤回后停止下发，并按保留策略清理/失效映射及缓存 |
| `GET /healthz` / `GET /readyz` | 运维探针 | 内网/探针权限 | 存活、配置快照就绪版本；不返回密钥或业务数据 |

SSP 管理变更写 `ssp_db` 与配置 outbox，由异步刷新器更新 Valkey/进程快照；管理 API 不直接改缓存作为唯一写入。Test token 只允许 sandbox tenant/placement/origin，token 生命周期短，SDK 内存之外不持久化。

### 2.4 SSP 发起的下游 API

| Method / path | 接收方 | 协议/认证 | 主要内容 |
|---|---|---|---|
| `POST {dsp.bid_url}` | DSP | OpenRTB 2.6、DSP 专属 credential/mTLS | 标准 BidRequest → BidResponse/no-bid；deadline 不超过 SSP 剩余 auction budget |
| `POST /internal/v1/audience:resolve` | DMP | JSON、SSP service identity/mTLS | pseudonymous token + consent/purpose → provider/segment IDs |
| `POST /internal/v1/events:batch` | DMP | JSON、SSP service identity/mTLS | 去敏事件批次；at-least-once、稳定 event ID |

DSP 返回的 `nurl`/`lurl`/`burl` 只允许命中该 DSP 注册并验证过的 callback host/egress policy，防止 SSP 被利用为 SSRF proxy。

### 2.5 User ID sync 规则

- SDK 可以在明确 consent 后申请/携带 publisher-scoped opaque subject；没有合法标识时不创建、读取或发送 identity。
- 如使用 cookie/redirect sync，必须是已登记 partner 的单次、短期、有状态流程，并回调到 `identity-links:complete`；禁止开放任意 redirect URL。浏览器第三方 cookie 不可用时不降级为 fingerprinting。
- `identity-sync` response 只给固定 partner URL 加不透明的一次性 state；DSP 回调必须使用 service identity，state 绑定 subject、partner、consent scope、expiry 且只能消费一次。禁止在 URL query 或 referer 中携带原始 ID。
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
| `POST /openrtb2/notices/win` | SSP | OpenRTB win notice payload | `nurl` 目标；记录 win 状态，不直接扣 advertiser spend |
| `POST /openrtb2/notices/loss` | SSP | OpenRTB loss notice payload | `lurl` 目标；记录 loss 状态 |
| `POST /openrtb2/notices/billing` | SSP | OpenRTB billing notice payload | `burl` 目标；校验 source event、币种/金额、合同策略后在事务中写 spend ledger 与更新预算 |
| `GET /healthz` / `GET /readyz` | 运维探针 | 无业务 body | 报告服务存活、配置版本/预算租约可用状态 |

通知 URL 由 DSP 在 BidResponse 对应 bid 的 `nurl`、`lurl`、`burl` 提供，SSP 按 OpenRTB 2.6 notice 语义调用；同一 notice 重放返回幂等成功。billing payload 的 `price` 宏替换和具体结算金额按 DSP/SSP 双边接入 profile 确定，不以 win price 自动替代 billable price。

### 3.2 DSP 管理/控制 API

所有管理 API 仅控制面可调用，采用管理员 RBAC，使用 ETag/version 做乐观锁；变更事务写入 `dsp_db` 和 config outbox，再由后台发布至 Valkey/节点快照。

| Method / path | 用途 | 主要数据 |
|---|---|---|
| `GET/POST /admin/v1/advertisers`、`PUT /admin/v1/advertisers/{id}` | 广告主管理 | 状态、允许币种 |
| `GET/POST /admin/v1/campaigns`、`PUT /admin/v1/campaigns/{id}` | Campaign 管理 | 状态、时间窗、目标 |
| `GET/POST /admin/v1/line-items`、`PUT /admin/v1/line-items/{id}` | 投放与预算配置 | targeting、bid、daily/lifetime budget、币种 |
| `GET/POST /admin/v1/creatives`、`PUT /admin/v1/creatives/{id}` | 素材与审核 | format、asset reference、review state |
| `POST /admin/v1/campaigns/{id}:publish` | 发布配置版本 | 校验配置并返回 `config_version`、发布状态 |
| `GET /admin/v1/budgets/{line_item_id}` | 查看预算账本汇总 | spent/reserved/available、currency、ledger version |

管理 API 不允许客户端直接调整 `spent` 汇总字段。财务调整必须生成带原因和审计身份的新 ledger entry。

## 4. DMP API

### 4.1 内部实时/批量接口

| Method / path | 调用方 | 请求/响应 | 语义 |
|---|---|---|---|
| `POST /internal/v1/audience:resolve` | SSP | namespace、伪名 token、tenant、purpose/consent、max segments → snapshot/provider/segments | 只读已发布快照；无匹配返回空集合；拒绝授权不返回受众 |
| `POST /internal/v1/events:batch` | SSP/授权事件生产方 | `batch_id`、schema version、events[] → accepted/rejected counts | 有界批量写 ClickHouse；至少一次投递，不承诺写入时唯一去重；分析按 event ID 去重 |
| `POST /internal/v1/snapshots` | 数据导入控制器 | provider、namespace、schema、source version、预期行数/checksum → snapshot/job ID | 创建不可变导入版本 |
| `POST /internal/v1/snapshots/{id}/members:batch` | 数据导入控制器 | batch ID、membership rows[] → accepted count | 批量导入，幂等重放；不直接对在线读取可见 |
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

- SSP exchange endpoint 使用 Supply 专属身份；DSP bidder endpoint 只接受注册 SSP 身份。服务身份不能跨租户或跨数据库权限复用。
- SDK 的 test token 由已认证 Portal 会话换取，绑定 tenant、placement、origin、scope、过期时间和请求额度；不将 long-lived secret 发给浏览器。
- CORS 精确列举 sandbox Portal origin，不使用 `*` 搭配 credentials；SDK API 做 payload 上限、placement allowlist、rate limit 和 abuse protection。
- render URL 为短期签名地址，host allowlist；iframe sandbox/CSP 阻止跨域创意访问 SSP cookie/storage。诊断信息默认脱敏。
- 浏览器 impression/click 上报仅作测量/调试输入，不直接产生财务分录。计费由 SSP/DSP 受信服务端策略和账本事务确认。
- OpenRTB 2.6 API 契约由 SSP 与 DSP 双边 profile 确定支持字段和宏；自有 SDK/Admin API 变更通过路径主版本和契约测试管理。