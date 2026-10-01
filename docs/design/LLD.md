# Open Ads 低层设计（LLD）

本文定义建议的聚合、端口、领域内 REST 契约、存储布局和失败语义。按服务整理的 API 路由与认证矩阵见 [API 设计](API.md)。数据库版本、驱动、部署拓扑和规模指标待评审后锁定。

## 1. Go 应用结构

三个应用分别位于独立仓库或独立 Go module；以下目录在各自仓库内，不建立共享领域包：

```text
cmd/<app>/main.go
internal/<app>/domain/          # 聚合、值对象、领域规则
internal/<app>/application/     # 用例、事务/并发编排
internal/<app>/ports/           # 仅本服务的存储和外部 API 接口
internal/<app>/adapter/http/    # net/http handler、wire DTO、映射
internal/<app>/adapter/store/   # 本服务数据库实现
internal/<app>/config/
internal/<app>/observability/
```

wire DTO 和 OpenRTB 类型留在对应服务 adapter 内。领域层不导入 `net/http`、Valkey 客户端、SQL 驱动或 ClickHouse 客户端。应用层依赖本应用定义的窄接口，依赖方向指向 domain/application。

建议端口形态（每个应用自行定义，不跨 module 共享）：

```go
type Clock interface { Now() time.Time }
type IDGenerator interface { NewID() string }
```

每个服务的 repository/client 接口使用自己的 domain 类型；不创建通用 `Repository[T]` 或 `common.Entity`。

## 2. SSP LLD

### 2.1 聚合与不变量

| 聚合根 | 主要内容 | 关键不变量 |
|---|---|---|
| Publisher | publisher 状态、允许的供给身份 | 暂停状态不得创建新 auction |
| PublisherScopedSubject | 单 publisher 范围的 SSP opaque subject ID 与 consent 生命周期 | 随机不可推导；不跨 publisher/tenant 复用；无授权时不建立/读取 |
| PartnerIdentityLink | 某 SSP subject 对某一个 DSP 的 pairwise buyer ID link | partner 唯一、consent 有效、过期/撤回立即停止下发 |
| Property | site/app 元数据、所属 publisher | 外部引用 publisher ID，不复制 Publisher 聚合 |
| Placement | 格式/尺寸、floor、currency、deal 和隐私/降级策略 | 格式约束合法；隐私策略不能被请求参数覆盖 |
| SupplyEndpoint | 入口身份、限流和协议配置 | 仅接受明确支持的 OpenRTB 版本/profile |
| AuctionPolicy | `at`、币种、出价校验、floor 和 tie-break 规则 | 结果必须符合 request 和 placement policy |
| Auction | 单次 request 的 deadline、有效候选和 auction outcome | request/imp/bid 关联正确；过期候选不参与选择；财务分录由独立结算用例创建 |
| PublisherPayableLedger | publisher 应付的不可变分录 | 只由有效 publisher-billable event 入账；同一 source event 不重复记账 |

Auction 是短生命周期对象，默认只存在于请求上下文中；竞价日志异步写去敏 delivery event，不持久化完整用户请求。Publisher payable ledger 是 SSP 自有的财务事实，不能与 DSP advertiser spend 共用模型。

### 2.2 OpenRTB 2.6 入口与 DSP 出站

- Supply 入口建议 `POST /openrtb2/auction`；对外名称可按供给方接入 profile 配置。
- DSP adapter 建议对每个 DSP 配置 `POST /openrtb2/bid` endpoint。发送/接收标准 OpenRTB 2.6 JSON，并按对端 profile 设置 `Content-Type`、`Accept` 和版本标记。
- 验证 `BidRequest.id` 非空、`imp` 非空且各 `imp.id` 唯一；只处理已声明支持的 impression 格式。校验响应 `BidResponse.id` 与 request id 相等，`seatbid[].bid[].impid` 必须指向原始 imp，出价币种、floor、尺寸/格式及 deadline 必须满足约定。
- 处理 no-bid 按 OpenRTB 2.x 语义实现，可包含 `seatbid: []`/`nbr` 等适用形式；DSP 错误、超时或无有效 bid 均不构成候选。
- 拍卖的 `at`、币种、price macro 展开和 notice 规则按 OpenRTB 2.6 与供给方约定实现。`nurl` 表示 win，`lurl` 表示 loss，`burl` 表示符合 exchange 计费策略的 billable event；不得将 win 自动等同于展示或计费。
- PublisherPayable 由 SSP 的 publisher 合同策略计算，可基于有效展示或合同定义的其他 billable event；金额和币种以整数微单位表达，记录策略版本和舍入结果。
- 不支持的 `ext` 忽略或按 profile 拒绝；不把私有字段放入标准字段。

### 2.2.1 SSP Client API 与 JavaScript SDK

Client API 是 SSP 面向测试门户/浏览器 SDK 的独立版本化契约，不是 OpenRTB；SDK 不直接请求 DSP、DMP 或任何数据库。API adapter 将请求映射为 SSP 自己的 placement/auction 命令，再由 SSP 生成合法 OpenRTB 2.6 BidRequest。第一阶段仅开放 sandbox/test 环境。

接口草案：

```http
POST /sdk/v1/ad-requests
Authorization: Bearer <short-lived-test-token>
Content-Type: application/json
Origin: https://portal.test.example
```

```json
{
  "request_id": "client-generated-uuid",
  "placement_id": "test-placement-id",
  "slot": { "id": "leaderboard", "width": 728, "height": 90 },
  "page": { "origin": "https://publisher.test", "path": "/demo" },
  "consent": { "source": "test-fixture", "value": null }
}
```

`request_id` 每次广告请求唯一；`page.path` 必须去除 query/fragment，禁止传原始 user/device ID。可选 `identity` 只含 SSP 签发的 opaque subject ID 或经 publisher 授权的 publisher-scoped pseudonymous ID。Consent 来自配置的测试 fixture 或正式 CMP adapter；SSP 校验 purpose 与 placement policy 后才可能查询 DMP/下发 buyer ID。SDK 传入的 consent 字符串本身不是可信授权证明，SSP 不在普通日志记录 consent value。

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
  },
  "diagnostics": {
    "auction_id": "opaque-test-id",
    "outcome": "bid",
    "duration_ms": 42
  }
}
```

无广告返回 `200` 和 `status: "no_fill"`，不把 no-fill 当成 transport error。`diagnostics` 只在 test token 有 `diagnostics:read` scope 时存在；去敏且不包含完整 OpenRTB payload、用户标识、DSP 密钥或未授权竞价数据。响应设置 `Cache-Control: no-store`；`render_url` 必须短时有效且指向受控 render host。

`POST /sdk/v1/identity` 在 consent/purpose 校验通过后创建或返回 publisher-scoped opaque subject ID；响应含 `subject_id`、`expires_at` 和 `scope`，不得回传原始 ID。仅凭 SDK 自报的 consent 不足以授权，生产接入需绑定经过验证的 CMP/publisher policy。用户撤回时 SDK/Publisher 调用删除流程，SSP 立即撤销 subject/link 与缓存。

原生 JS SDK 不强依赖 UI 框架或广告 vendor SDK，首版以 ES module 发布，Portal 加载同一版本构建产物。建议调用面：

```js
const sdk = new OpenAdsSDK({
  baseUrl: "https://ssp.test.example",
  placementId: "test-placement-id",
  getAccessToken: () => portalSession.getEphemeralTestToken()
});

const result = await sdk.requestAd({
  slot: { id: "leaderboard", width: 728, height: 90 },
  consent: testConsentFixture
});

if (result.status === "filled") {
  await sdk.render(document.querySelector("#ad-slot"), result.ad);
}
```

SDK 最小能力：request/timeout/abort、no-fill/error 状态、请求 ID、一次性 render/destroy、事件回调和可选 test diagnostics。禁止默默重试广告请求导致重复 auction；调用方需显式发起新请求。渲染通过受控 HTTPS `render_url` 的 sandboxed iframe 完成；不对 SSP 返回的 markup 使用裸 `innerHTML`，iframe 不授予 `allow-same-origin`，设置 `referrerpolicy="no-referrer"`。不保存 bearer token 到 localStorage/cookie；token 只驻留内存。

浏览器上报的 impression/click 只能作为客户端测量信号，不是财务可信事实；不得仅凭 JS event 增加 PublisherPayableLedger 或 AdvertiserSpendLedger。真正 billable event 必须来自 SSP 受信的 tracker/验证策略或双方约定的 server-side notice，并在对应账本幂等入账。

### 2.2.1.1 OpenRTB identity mapping

- `user.id` 使用 SSP exchange-scoped opaque ID；除非 buyer/seller 另有明确约定，不放 publisher 原始账号 ID。
- 每个 DSP 的 `user.buyeruid` 从 SSP `PartnerIdentityLink` 独立查出；DSP A 的 buyer ID 不得发送给 DSP B。无有效 link 时省略 `buyeruid`。
- `user.eids` 仅传已登记 provider/source 的 EID，且须有相应授权和业务协议；EID 不用于绕过缺失的 buyeruid consent。
- 对 DMP 的 `subject_token` 为 HMAC-SHA256(`key_version`, tenant, publisher namespace, SSP subject ID)；DMP 按相同 namespace 查询 membership，只回 segments，不回 canonical user ID。密钥轮换通过 key version/双读迁移完成，不把密钥发给 DMP。
- OpenRTB request 的 consent 字段按适用协议传递；原始 consent string 不写普通日志。没有有效身份或 consent 时删除 `user.id`/`buyeruid`/相关 EID 后继续 no-ID auction，或按 placement policy no-bid。

### 2.2.2 Test Portal

Portal 是独立静态页面/文件包，不增加 Go 业务服务、数据库或领域模型；在测试环境由静态站点托管。登录通过受保护的测试会话，之后调用 `POST /portal/v1/test-tokens` 获取短期 token。token scope 限定 test tenant、允许的 placement、Portal origin、速率和到期时间；签发接口不得向普通生产站点开放。

测试页面提供：选择 test tenant/placement/slot 尺寸、加载 consent fixture、请求/重试一次广告、渲染或清空广告位、显示 request ID/状态/耗时/去敏诊断摘要、展示 no-fill/error。默认不自动向正式 DSP 或财务链路制造 billable event；需要演示埋点时使用明确标注的 sandbox test event。

安全边界：SSP 对 Portal origin 做精确 CORS allowlist；不使用 `*` 搭配 credentials。短期 token 不含服务端密钥；接口校验 placement 与 token tenant 绑定，实施 payload 上限、QPS 配额、重放/滥用防护。Portal 和 renderer 使用 CSP；跨域 iframe sandbox 禁止访问 SSP origin/cookie。测试环境与生产环境 endpoint、数据库及 token issuer 分离。

### 2.3 DMP 受众解析契约（内部 API，不是 OpenRTB）

```http
POST /internal/v1/audience:resolve
Content-Type: application/json
Authorization: service identity
```

请求字段：`request_id`、`tenant_id`、`provider_id`（可选）、`namespace`、`subject_token`、`consent_context`、`purposes`、`max_segments`。`subject_token` 是按 namespace 生成的伪名化标识，不传原始邮箱、电话或原始设备 ID。无合法处理依据时 SSP 不调用此 API。

响应字段：`request_id`、`snapshot_id`、`provider_id`、`segments: [{id}]`、`resolved_at`。返回值按 `max_segments` 截断，segments 的 `id`/`name` 可映射到 OpenRTB `user.data[].segment[]`。DMP 不向 SSP 暴露 profile 细节、分数或 ClickHouse 字段。

错误语义：400 表示契约无效；401/403 表示服务身份/权限失败；429 表示限流；503 表示暂不可用；超时由调用方截止时间控制。合法请求但无匹配受众返回空 segments，不返回 404。

### 2.4 SSP MariaDB 持久化

SSP 的领域事实写入独立的 MariaDB 数据库 `ssp_db`。管理/控制面读写 MariaDB；RTB 热路径读预热到进程和 Valkey 的版本化快照，不在缓存 miss 时回源数据库。

```sql
CREATE TABLE publisher (
  tenant_id BINARY(16) NOT NULL,
  publisher_id BINARY(16) NOT NULL,
  status VARCHAR(16) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, publisher_id)
) ENGINE=InnoDB;

CREATE TABLE property (
  tenant_id BINARY(16) NOT NULL,
  property_id BINARY(16) NOT NULL,
  publisher_id BINARY(16) NOT NULL,
  property_type VARCHAR(16) NOT NULL,
  external_id VARCHAR(128) NOT NULL,
  status VARCHAR(16) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, property_id),
  UNIQUE KEY uq_property_external (tenant_id, property_type, external_id),
  KEY idx_property_publisher (tenant_id, publisher_id, status),
  CONSTRAINT fk_property_publisher FOREIGN KEY (tenant_id, publisher_id)
    REFERENCES publisher (tenant_id, publisher_id)
) ENGINE=InnoDB;

CREATE TABLE placement (
  tenant_id BINARY(16) NOT NULL,
  placement_id BINARY(16) NOT NULL,
  property_id BINARY(16) NOT NULL,
  media_type VARCHAR(24) NOT NULL,
  floor_micros BIGINT UNSIGNED NOT NULL,
  currency CHAR(3) NOT NULL,
  privacy_policy VARCHAR(24) NOT NULL,
  status VARCHAR(16) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, placement_id),
  KEY idx_placement_property (tenant_id, property_id, status),
  CONSTRAINT fk_placement_property FOREIGN KEY (tenant_id, property_id)
    REFERENCES property (tenant_id, property_id)
) ENGINE=InnoDB;

CREATE TABLE supply_endpoint (
  tenant_id BINARY(16) NOT NULL,
  endpoint_id BINARY(16) NOT NULL,
  property_id BINARY(16) NOT NULL,
  protocol_version VARCHAR(16) NOT NULL,
  auth_secret_ref VARCHAR(255) NULL,
  status VARCHAR(16) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (tenant_id, endpoint_id),
  KEY idx_endpoint_property (tenant_id, property_id, status),
  CONSTRAINT fk_endpoint_property FOREIGN KEY (tenant_id, property_id)
    REFERENCES property (tenant_id, property_id)
) ENGINE=InnoDB;

CREATE TABLE ssp_config_outbox (
  event_id BINARY(16) NOT NULL,
  tenant_id BINARY(16) NOT NULL,
  aggregate_type VARCHAR(32) NOT NULL,
  aggregate_id BINARY(16) NOT NULL,
  aggregate_version BIGINT UNSIGNED NOT NULL,
  created_at DATETIME(6) NOT NULL,
  published_at DATETIME(6) NULL,
  PRIMARY KEY (event_id),
  KEY idx_ssp_outbox_pending (published_at, created_at)
) ENGINE=InnoDB;

CREATE TABLE publisher_subject (
  tenant_id BINARY(16) NOT NULL,
  publisher_id BINARY(16) NOT NULL,
  subject_id BINARY(16) NOT NULL,
  source_namespace VARCHAR(128) NULL,
  source_subject_hmac BINARY(32) NULL,
  hmac_key_version SMALLINT UNSIGNED NULL,
  status VARCHAR(16) NOT NULL,
  consent_version VARCHAR(64) NOT NULL,
  consent_purpose_mask BIGINT UNSIGNED NOT NULL,
  issued_at DATETIME(6) NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  revoked_at DATETIME(6) NULL,
  PRIMARY KEY (tenant_id, publisher_id, subject_id),
  UNIQUE KEY uq_publisher_subject_source (tenant_id, publisher_id, source_namespace, source_subject_hmac),
  KEY idx_subject_expiry (tenant_id, expires_at),
  CONSTRAINT fk_subject_publisher FOREIGN KEY (tenant_id, publisher_id)
    REFERENCES publisher (tenant_id, publisher_id)
) ENGINE=InnoDB;

CREATE TABLE partner_identity_link (
  tenant_id BINARY(16) NOT NULL,
  publisher_id BINARY(16) NOT NULL,
  subject_id BINARY(16) NOT NULL,
  partner_id VARCHAR(128) NOT NULL,
  partner_user_id_ciphertext VARBINARY(1024) NOT NULL,
  partner_user_id_hmac BINARY(32) NOT NULL,
  encryption_key_version SMALLINT UNSIGNED NOT NULL,
  match_method VARCHAR(24) NOT NULL,
  consent_version VARCHAR(64) NOT NULL,
  consent_purpose_mask BIGINT UNSIGNED NOT NULL,
  linked_at DATETIME(6) NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  revoked_at DATETIME(6) NULL,
  PRIMARY KEY (tenant_id, publisher_id, subject_id, partner_id),
  UNIQUE KEY uq_partner_identity (tenant_id, publisher_id, partner_id, partner_user_id_hmac),
  KEY idx_partner_link_expiry (tenant_id, partner_id, expires_at),
  CONSTRAINT fk_link_subject FOREIGN KEY (tenant_id, publisher_id, subject_id)
    REFERENCES publisher_subject (tenant_id, publisher_id, subject_id)
) ENGINE=InnoDB;
```

控制面在一个 MariaDB 事务内提交聚合变更和 `ssp_config_outbox` 记录。后台刷新器消费未发布变更、装载对应聚合及必要引用、生成不可变快照，再发布到 Valkey/本地内存；重复消费按 aggregate version 幂等处理。publisher subject 的外部 pseudonym 仅存 HMAC 索引，不存明文。identity link 中 partner ID 只以应用层 envelope encryption 密文保存，并带 encryption key version；HMAC 索引使用独立密钥，不能从 HMAC 反推出原值。只允许 SSP identity service 解密，不写日志。表结构是概念草案，软删除、Deal/AuctionPolicy 表和索引需按管理查询模型细化。

`publisher_subject.source_subject_hmac` 用于将重复 publisher-scoped pseudonym 稳定映射到 SSP subject；无外部 ID 时由 SSP 生成随机 `subject_id`，consent 允许后由 SDK 保存在 publisher origin 的 first-party storage。optional redirect sync 的 single-use state 只存在有 TTL 的 Valkey entry，与 subject、partner、consent version 绑定，DSP callback 成功后立即删除。

### 2.5 Valkey 热缓存布局

Valkey 不是 SSP/DSP 业务事实源。缓存值必须包含 `schema_version`、聚合/快照版本和生成时间，支持过期、版本比较、全量重建；缓存只保留 RTB 决策所需字段。

```text
ssp:cache:v1:{tenant}:publisher:<publisher_id>       -> versioned publisher snapshot
ssp:cache:v1:{tenant}:property:<property_id>          -> versioned property snapshot
ssp:cache:v1:{tenant}:placement:<placement_id>        -> versioned placement snapshot
ssp:cache:v1:{tenant}:idx:property-placements        -> rebuildable placement IDs
ssp:cache:v1:{tenant}:aud:<namespace>:<token-hash>    -> segment IDs + snapshot_id, short TTL
ssp:cache:v1:{tenant}:identity:<publisher>:<subject>:<partner> -> encrypted buyeruid + consent/version/expiry
dsp:cache:v1:{tenant}:campaign:<campaign_id>          -> versioned compiled campaign snapshot
dsp:cache:v1:{tenant}:lineitem:<line_item_id>          -> versioned compiled targeting/budget snapshot
dsp:cache:v1:{tenant}:idx:active-lineitems:<version>  -> rebuildable candidate index
```

- SSP/DSP 使用不同 ACL 身份和 key prefix；若共享 Valkey 集群，不依赖 `SELECT` 数据库编号实现隔离。集群多 key 原子操作需满足同 slot 要求。
- SSP/DSP 服务启动时先从各自 MariaDB 装载一致快照并校验版本，再更新 Valkey 和本地只读快照；依赖快照未就绪时 readiness 为失败，不接 RTB 流量。
- 控制面事务通过数据库 outbox 驱动缓存刷新。缓存写入使用版本比较，旧事件不能覆盖新版本；周期性 reconciliation 可从 MariaDB 修复丢失/过期缓存。
- RTB handler 优先使用进程内不可变快照；必要时读 Valkey，但任何缓存 miss 都不能触发同步 MariaDB 查询。若无未过期本地快照，则对受影响 placement/line item no-bid，并告警。
- DSP 预算 lease 在后台以事务分配并缓存剩余额度；bid 热路径只原子扣减本地租约额度，lease 不足时停止该 line item 出价并异步申请补充。
- 受众缓存只存最小 segment ID、snapshot ID 和短 TTL；缓存 key 使用 HMAC/token hash，不保存可识别原值。
- identity link cache 使用 publisher/subject/partner 组合键；value 中的 buyeruid 仍保持密文，只有 SSP 在构建对应 DSP 请求时解密。subject/link 撤回或过期时先写拒绝使用状态并删缓存，再异步清理 MariaDB/下游映射，避免传播延迟期间继续发送。
- SSP 竞价 outcome Stream 若启用，只是短期写缓冲而非业务事实源；事件持久化/允许丢失比例需单独定案。配置 outbox 的权威记录仍在 `ssp_db`。

### 2.6 SSP 配信日志与 PublisherPayableLedger

SSP delivery log 记录 auction、bid、win/loss、impression、click、billable、invalid/reversal 等追加事件。竞价诊断事件走有界异步批量写入，不延迟 RTB response；billable event 在独立的 tracker/notice handler 中验证并与 publisher payable 分录同事务写入 `ssp_db`。财务入账失败必须重试或进入可告警隔离队列，不能静默丢弃。

每个外部回调/展示事件生成稳定 `source_event_key`：优先使用来源方不可变 event ID；没有 event ID 时，以 SSP/DSP partner ID、OpenRTB auction/request ID、bid ID、imp ID、event type 和合同约定的 billable sequence 形成规范键后做 SHA-256。重试必须复用同一键；人工冲正使用新键并引用原始 event。禁止用接收时间或随机 UUID 作为唯一幂等依据。

```sql
CREATE TABLE ssp_delivery_event (
  tenant_id BINARY(16) NOT NULL,
  event_id BINARY(16) NOT NULL,
  source_event_key BINARY(32) NOT NULL,
  event_type VARCHAR(24) NOT NULL,
  auction_id VARCHAR(128) NOT NULL,
  imp_id VARCHAR(128) NOT NULL,
  publisher_id BINARY(16) NOT NULL,
  property_id BINARY(16) NOT NULL,
  placement_id BINARY(16) NOT NULL,
  dsp_id VARCHAR(128) NULL,
  creative_id VARCHAR(128) NULL,
  currency CHAR(3) NULL,
  amount_micros BIGINT NULL,
  occurred_at DATETIME(6) NOT NULL,
  received_at DATETIME(6) NOT NULL,
  schema_version SMALLINT UNSIGNED NOT NULL,
  PRIMARY KEY (tenant_id, event_id),
  UNIQUE KEY uq_ssp_event_source (tenant_id, source_event_key),
  KEY idx_ssp_event_auction (tenant_id, auction_id, imp_id, occurred_at),
  KEY idx_ssp_event_publisher_time (tenant_id, publisher_id, occurred_at)
) ENGINE=InnoDB;

CREATE TABLE publisher_payable_ledger (
  tenant_id BINARY(16) NOT NULL,
  entry_id BINARY(16) NOT NULL,
  idempotency_key BINARY(32) NOT NULL,
  source_event_id BINARY(16) NOT NULL,
  publisher_id BINARY(16) NOT NULL,
  auction_id VARCHAR(128) NOT NULL,
  imp_id VARCHAR(128) NOT NULL,
  entry_type VARCHAR(16) NOT NULL,
  amount_micros BIGINT NOT NULL,
  currency CHAR(3) NOT NULL,
  publisher_policy_version BIGINT UNSIGNED NOT NULL,
  occurred_at DATETIME(6) NOT NULL,
  recorded_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, entry_id),
  UNIQUE KEY uq_publisher_payable_idempotency (tenant_id, idempotency_key),
  KEY idx_publisher_payable_period (tenant_id, publisher_id, occurred_at),
  CONSTRAINT fk_publisher_payable_event FOREIGN KEY (tenant_id, source_event_id)
    REFERENCES ssp_delivery_event (tenant_id, event_id)
) ENGINE=InnoDB;
```

一个事务中先插入唯一的 billable `ssp_delivery_event`，再追加正向 payable 分录并更新可重建的 publisher-period summary。重复 source event 命中唯一键后返回幂等成功，不再次增加应付。冲正/人工调整写新分录并引用原事件，不能修改或删除历史账本。金额采用有符号整数，冲正为负；对外结算按货币最小单位舍入规则导出。实际 publisher payout/remittance 及付款状态属于后续独立用例，不等同于本账本中的应付金额。

delivery event 是高写入量数据，需批量落库、定义保留/归档周期和按租户/日期查询索引；账本保留期限由财务审计要求决定。MariaDB 分区策略需另外验证唯一键约束，不应为分区而破坏全局幂等性。

## 3. DSP LLD

### 3.1 聚合与领域规则

| 聚合根 | 主要内容 | 关键不变量 |
|---|---|---|
| Advertiser | 广告主状态、允许币种 | 暂停广告主下 line item 不出价 |
| Campaign | 活动状态、投放周期、投放目标 | 活动时间窗和总预算有效 |
| LineItem | 预算、bid strategy、targeting、频次/日限额 | 只有有效且预算租约充足时可候选 |
| Creative | 素材格式、落地页、审核状态 | 素材须匹配 impression 格式和审核规则 |
| BudgetAccount | 已花费、预留、可用金额及版本 | 可用金额不小于零；重复 billable event 不重复扣减 |
| DeliveryEvent | bid/win/loss/billable/impression 等 DSP 本地事件 | 每个 source event 唯一；win event 本身不扣预算 |
| AdvertiserSpendLedger | advertiser/campaign/line item 的不可变 spend 分录 | 仅对有效 billable event 记账；source event 幂等 |

Targeting DSL 属 DSP 私有领域语言；仅从 OpenRTB request 读取标准上下文，不将 DMP segment 解释为 DMP 实体，只按 `data.id`/`segment.id` 配置匹配。
金额/预算模型属于 DSP bounded context；不读取 SSP 的 PublisherPayableLedger，也不将 publisher 应付金额当作 advertiser spend。

### 3.2 MariaDB 表草案

DSP 使用独立 MariaDB 数据库 `dsp_db`，与 SSP 的 `ssp_db` 分开授权、连接池、迁移和备份。全部业务表使用 InnoDB、明确主键、UTC 时间和外键/唯一约束；tenant 条件需进入唯一键与查询索引。

```sql
CREATE TABLE advertiser (
  tenant_id BINARY(16) NOT NULL,
  advertiser_id BINARY(16) NOT NULL,
  status VARCHAR(16) NOT NULL,
  default_currency CHAR(3) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, advertiser_id)
) ENGINE=InnoDB;

CREATE TABLE campaign (
  tenant_id BINARY(16) NOT NULL,
  campaign_id BINARY(16) NOT NULL,
  advertiser_id BINARY(16) NOT NULL,
  status VARCHAR(16) NOT NULL,
  starts_at DATETIME(6) NOT NULL,
  ends_at DATETIME(6) NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, campaign_id),
  KEY idx_campaign_advertiser (tenant_id, advertiser_id, status),
  CONSTRAINT fk_campaign_advertiser FOREIGN KEY (tenant_id, advertiser_id)
    REFERENCES advertiser (tenant_id, advertiser_id)
) ENGINE=InnoDB;

CREATE TABLE line_item (
  tenant_id BINARY(16) NOT NULL,
  line_item_id BINARY(16) NOT NULL,
  campaign_id BINARY(16) NOT NULL,
  status VARCHAR(16) NOT NULL,
  currency CHAR(3) NOT NULL,
  bid_micros BIGINT UNSIGNED NOT NULL,
  daily_budget_micros BIGINT UNSIGNED NOT NULL,
  lifetime_budget_micros BIGINT UNSIGNED NOT NULL,
  targeting_version BIGINT UNSIGNED NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, line_item_id),
  KEY idx_line_campaign (tenant_id, campaign_id, status),
  CONSTRAINT fk_line_campaign FOREIGN KEY (tenant_id, campaign_id)
    REFERENCES campaign (tenant_id, campaign_id)
) ENGINE=InnoDB;

CREATE TABLE creative (
  tenant_id BINARY(16) NOT NULL,
  creative_id BINARY(16) NOT NULL,
  status VARCHAR(16) NOT NULL,
  format VARCHAR(24) NOT NULL,
  payload MEDIUMTEXT NOT NULL,
  payload_sha256 BINARY(32) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (tenant_id, creative_id),
  KEY idx_creative_format (tenant_id, format, status)
) ENGINE=InnoDB;

CREATE TABLE line_item_creative (
  tenant_id BINARY(16) NOT NULL,
  line_item_id BINARY(16) NOT NULL,
  creative_id BINARY(16) NOT NULL,
  PRIMARY KEY (tenant_id, line_item_id, creative_id),
  CONSTRAINT fk_lic_line FOREIGN KEY (tenant_id, line_item_id)
    REFERENCES line_item (tenant_id, line_item_id),
  CONSTRAINT fk_lic_creative FOREIGN KEY (tenant_id, creative_id)
    REFERENCES creative (tenant_id, creative_id)
) ENGINE=InnoDB;

CREATE TABLE budget_account (
  tenant_id BINARY(16) NOT NULL,
  line_item_id BINARY(16) NOT NULL,
  currency CHAR(3) NOT NULL,
  spent_micros BIGINT UNSIGNED NOT NULL,
  leased_micros BIGINT UNSIGNED NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, line_item_id),
  CONSTRAINT fk_budget_line FOREIGN KEY (tenant_id, line_item_id)
    REFERENCES line_item (tenant_id, line_item_id)
) ENGINE=InnoDB;

CREATE TABLE budget_lease (
  tenant_id BINARY(16) NOT NULL,
  lease_id BINARY(16) NOT NULL,
  line_item_id BINARY(16) NOT NULL,
  owner_instance VARCHAR(128) NOT NULL,
  amount_micros BIGINT UNSIGNED NOT NULL,
  remaining_micros BIGINT UNSIGNED NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, lease_id),
  KEY idx_lease_expiry (expires_at),
  KEY idx_lease_owner (tenant_id, owner_instance, line_item_id)
) ENGINE=InnoDB;

CREATE TABLE dsp_delivery_event (
  tenant_id BINARY(16) NOT NULL,
  event_id BINARY(16) NOT NULL,
  source_event_key BINARY(32) NOT NULL,
  event_type VARCHAR(24) NOT NULL,
  auction_id VARCHAR(128) NOT NULL,
  bid_id VARCHAR(128) NOT NULL,
  imp_id VARCHAR(128) NOT NULL,
  advertiser_id BINARY(16) NOT NULL,
  campaign_id BINARY(16) NOT NULL,
  line_item_id BINARY(16) NOT NULL,
  creative_id BINARY(16) NULL,
  currency CHAR(3) NOT NULL,
  amount_micros BIGINT NULL,
  occurred_at DATETIME(6) NOT NULL,
  received_at DATETIME(6) NOT NULL,
  schema_version SMALLINT UNSIGNED NOT NULL,
  PRIMARY KEY (tenant_id, event_id),
  UNIQUE KEY uq_dsp_event_source (tenant_id, source_event_key),
  KEY idx_dsp_event_auction (tenant_id, auction_id, imp_id, occurred_at),
  KEY idx_dsp_event_line_time (tenant_id, line_item_id, occurred_at)
) ENGINE=InnoDB;

CREATE TABLE advertiser_spend_ledger (
  tenant_id BINARY(16) NOT NULL,
  entry_id BINARY(16) NOT NULL,
  idempotency_key BINARY(32) NOT NULL,
  source_event_id BINARY(16) NOT NULL,
  advertiser_id BINARY(16) NOT NULL,
  campaign_id BINARY(16) NOT NULL,
  line_item_id BINARY(16) NOT NULL,
  entry_type VARCHAR(16) NOT NULL,
  amount_micros BIGINT NOT NULL,
  currency CHAR(3) NOT NULL,
  occurred_at DATETIME(6) NOT NULL,
  recorded_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, entry_id),
  UNIQUE KEY uq_advertiser_spend_idempotency (tenant_id, idempotency_key),
  KEY idx_advertiser_spend_period (tenant_id, advertiser_id, occurred_at),
  KEY idx_line_spend_period (tenant_id, line_item_id, occurred_at),
  CONSTRAINT fk_advertiser_spend_event FOREIGN KEY (tenant_id, source_event_id)
    REFERENCES dsp_delivery_event (tenant_id, event_id)
) ENGINE=InnoDB;
```

上面是概念 schema；字符集、外键级联策略、campaign/line item 的生命周期删除方式和价格单位需迁移设计评审，不直接视为可执行最终迁移。`dsp_delivery_event` 是投放/回调事件日志，`advertiser_spend_ledger` 是 DSP advertiser 的费用事实；两者不可互相替代。

DSP 的 spend 幂等键优先继承 SSP/exchange 提供的稳定 billable event ID；若协议接入没有该字段，DSP 与交易对手必须共同约定基于 auction ID、bid ID、imp ID 和 billable sequence 的确定性规则。不能直接以 `nurl` 调用次数或任意重试次数当收费次数。

### 3.3 预算并发与竞价

1. 配置变更由 MariaDB 事务提交并增加版本；DSP 节点通过轮询变更版本/管理 API 获取完整一致快照，校验后原子替换本地只读快照。
2. 节点出价只使用尚未过期的本地快照和预算 lease。lease 由 MariaDB 条件更新/事务分配，实例不得使用超过剩余额度的租约金额。
3. 收到 `nurl` win notice 后记录非计费 `win` delivery event，不扣 advertiser 预算。收到 SSP 按约定触发的有效 `burl` billable notice 后，在一个 InnoDB 事务中插入 billable event 和 `advertiser_spend_ledger` 分录，并更新 `spent_micros`/lease 金额。唯一幂等键冲突表示重放，返回幂等成功；事务失败则重试。
4. lease 到期回收需检查 owner 与剩余金额，使用锁/条件版本避免与结算竞争。实例失联会造成额度暂时不可用而非超支。
5. 出价热路径不得等待慢 SQL。快照过旧、lease 不可用或预算状态不确定时，该 line item no-bid。

### 3.4 DSP HTTP 契约

- `POST /openrtb2/bid`：OpenRTB 2.6 BidRequest，返回标准 BidResponse 或标准 no-bid。解析/评估受请求 deadline 限制。
- `GET /healthz`、`GET /readyz`：仅报告进程健康和关键依赖/快照准备度，不暴露业务数据。
- BidResponse 的 `nurl`、`lurl`、`burl` 分别指向 DSP 的 win、loss、billable notice endpoint；SSP 按 OpenRTB 2.6 notice 的 HTTP POST 语义和受支持宏发送。回调处理幂等。`nurl`/`lurl` 更新 delivery state，只有符合 DSP 计费策略的 `burl` 产生 spend。
- Campaign CRUD/审核管理 API 不属于 bidder endpoint，可作为后续独立控制面需求，不在第一期默认范围。

### 3.5 DSP 热缓存与零 DB 读取热路径

- DSP 配置事务同时写入 `dsp_config_outbox`；后台刷新器读取新版本，编译 campaign/line item/targeting 快照，写入 `dsp:cache:*` 并原子替换各实例的进程内快照。
- `POST /openrtb2/bid` 不查询 `dsp_db`。不得在 cache miss 时退回 SQL；缓存/本地快照缺失或超过最大陈旧时间时，该实例不就绪或对对应 line item no-bid。
- 预算租约在后台批量申请，事务预留 MariaDB 中的可用预算；RTB 只消费已预留的本地额度。有效 Billing Notice 使用 MariaDB 事务幂等增加 advertiser spend 并扣预算；Win Notice 只记 win，不扣费。成功结算后异步补足租约。
- 发布配置时监控实例已加载版本；过期实例从流量池摘除或只禁用相关 line item，不能继续使用无限期旧快照。
- 缓存重建由 `dsp_db` 权威数据驱动。Valkey 故障时，已有有效进程内快照可按上限继续服务；节点重启后未完成预热前不 ready。

## 4. DMP LLD

### 4.1 聚合与发布流程

| 概念 | DMP 责任 |
|---|---|
| DataProvider | provider 标识、身份 namespace 和数据来源许可版本 |
| IdentityNamespace | 标识格式/散列策略/保留策略；不共享 SSP/DSP user entity |
| ConsentEvent | 追加记录的授权/撤回事件和依据版本；不覆盖历史事件 |
| SegmentDefinitionVersion | 不可变 segment 定义版本、provider ID、发布时间 |
| MembershipSnapshot | 某 provider/namespace 下不可变 subject-token 到 segment-ID 映射 |
| SnapshotPublication | 只有校验完成的 snapshot 才能被解析 API 选择 |
| AudienceResolution | 单次查询的输入依据、snapshot ID、结果数量和耗时（去敏审计） |

发布过程：创建新 `snapshot_id` → 批量写 membership → 校验行数/校验和/重复率 → 写入 publication marker → DMP API 刷新可用快照指针。任何一步失败时旧快照继续服务。旧快照回收按保留期限和活跃引用处理。

### 4.2 ClickHouse 表草案

ClickHouse 存储以追加写、批量写和不可变版本为主。以下键为访问模式的起点，最终 `PARTITION BY`/`ORDER BY` 必须用真实基数和查询压测校正。

```sql
CREATE TABLE dmp_membership_snapshot
(
  tenant_id UUID,
  provider_id String,
  namespace LowCardinality(String),
  snapshot_id UUID,
  snapshot_month Date,
  subject_token FixedString(64),
  subject_token_key_version UInt16,
  segment_id String,
  ingested_at DateTime64(3, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(snapshot_month)
ORDER BY (tenant_id, snapshot_id, namespace, subject_token, segment_id);

CREATE TABLE dmp_snapshot_publication
(
  tenant_id UUID,
  provider_id String,
  namespace LowCardinality(String),
  snapshot_id UUID,
  published_at DateTime64(3, 'UTC'),
  expected_rows UInt64,
  checksum String
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(published_at)
ORDER BY (tenant_id, provider_id, namespace, published_at, snapshot_id);

CREATE TABLE dmp_segment_definition_version
(
  tenant_id UUID,
  provider_id String,
  segment_id String,
  version UInt64,
  name String,
  status LowCardinality(String),
  valid_from DateTime64(3, 'UTC'),
  written_at DateTime64(3, 'UTC')
)
ENGINE = MergeTree
ORDER BY (tenant_id, provider_id, segment_id, version);

CREATE TABLE dmp_identity_revocation
(
  tenant_id UUID,
  namespace LowCardinality(String),
  subject_token FixedString(64),
  subject_token_key_version UInt16,
  revocation_id UUID,
  effective_at DateTime64(3, 'UTC'),
  received_at DateTime64(3, 'UTC'),
  reason_code LowCardinality(String)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(received_at)
ORDER BY (tenant_id, namespace, subject_token, effective_at, revocation_id);

CREATE TABLE dmp_event
(
  tenant_id UUID,
  event_date Date,
  event_id UUID,
  event_type LowCardinality(String),
  provider_id String,
  namespace LowCardinality(String),
  subject_token FixedString(64),
  segment_id String,
  occurred_at DateTime64(3, 'UTC'),
  ingested_at DateTime64(3, 'UTC'),
  schema_version UInt16
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(event_date)
ORDER BY (tenant_id, event_date, event_type, event_id)
TTL event_date + INTERVAL 13 MONTH DELETE;
```

`FixedString(64)` 假设 token 是 64 位十六进制 SHA-256；若使用二进制 digest 或 keyed HMAC，按真实编码调整。`namespace` 等低基数枚举可考虑 `LowCardinality`，高基数 subject ID 不应如此编码。事件 TTL `13 MONTH` 只是示例占位，必须由合规/报表需求批准。

受众查询先解析当前已发布 snapshot，然后限定 `tenant_id + snapshot_id + namespace + subject_token + subject_token_key_version` 做精确匹配并限制 segment 数量；同查询/受控 deny set 必须排除有效 revocation tombstone。snapshot id 可在进程内短时间缓存；撤回标记不得依赖长 TTL 缓存，撤回会触发 deny set 失效/刷新。物理删除由重建 membership snapshot 和受控 TTL/分区清理完成。上线前必须用最大分区、撤回比例和生产级基数做 P95/P99/并发压测。

事件至少一次：相同 `event_id` 可因重试重复写入；分析查询必须采用 `uniqExact(event_id)` 或明确的去重层。不得假设 MergeTree 会提供唯一约束或同步去重；物化视图不能被当作源表去重保障。

### 4.3 DMP HTTP 契约

| Endpoint | 用途 | 写入/响应规则 |
|---|---|---|
| `POST /internal/v1/audience:resolve` | 实时查询某 token 的 segment IDs | 限制结果数；无结果返回空集合；只查已发布快照 |
| `POST /internal/v1/identity:revoke` | 登记某 namespace subject 的撤回 | 先持久化不可变 tombstone 并使在线解析拒绝该 token；历史快照中的物理删除异步完成并报告状态 |
| `POST /internal/v1/events:batch` | 批量写入去敏事件 | 有界批量；成功仅在 ClickHouse insert 达到约定持久化确认后返回；允许重复投递 |
| `POST /internal/v1/snapshots` | 创建/登记导入任务（仅在控制面开放时） | 版本化；不可直接发布未校验快照 |
| `POST /internal/v1/snapshots/{id}:publish` | 校验完成后发布 | 校验租户/provider/namespace/行数/校验和后追加 publication marker |

实时查询输入至少包含 request ID、租户、namespace、subject token 和 consent context。DMP 对不允许的目的返回空结果或明确 policy denial；SSP 必须 fail closed，不得把拒绝解释为可继续带受众出价。

### 4.4 ClickHouse 运行约束

- membership 和 event 都使用批量写入，避免逐行 insert；批大小/等待时间由吞吐与新鲜度压测确定。
- snapshot 用 immutable ID 定址；不在实时查询中对全表执行 `FINAL` 或对全部历史版本做全量聚合。
- publication marker 只在快照核验完成后写入；完整性核验需有独立导入任务 ID 和重跑策略。
- TTL、分区删除、备份恢复、集群副本一致性和 DDL 变更策略须随部署拓扑评审。
- 若精确 subject lookup 未满足在线延迟，第一优化顺序为键布局/数据压缩/副本与 SSP TTL cache；再评估预热/物化结构。仍不达标则须重新讨论存储约束或同步服务边界。

## 5. OpenRTB 与内部契约测试

每个服务各自维护规范样例和独立测试，不引用另一个服务的领域类型：

- OpenRTB 2.6 bid request/response 样例的 JSON encode/decode round-trip。
- Client API contract 测试覆盖 `filled`/`no_fill`/错误状态、请求 ID、超时/取消、响应 cache headers 与版本兼容。
- Portal/SDK 安全测试覆盖过期或越权 test token、tenant/placement 不匹配、非 allowlisted Origin、payload 上限、rate limit 和重放。
- Renderer 测试覆盖恶意 markup、跨域 iframe sandbox/CSP、destroy 清理、过期 render URL；客户端伪造 impression/click 不得生成财务 ledger entry。
- Portal smoke test 覆盖登录换 token、加载 SDK、请求广告、渲染及 no-fill 诊断；diagnostics scope 缺失时响应不含调试字段。
- 请求 `id`、imp ID、`tmax`、`at`、currency、`user.data[].segment[]` 的映射与边界测试。
- DSP 响应 request ID 不匹配、未知 imp、非法价格/币种、超时、空 seatbid 和无效 creative 的拒绝/no-bid 测试。
- DMP segment 映射保留 provider/segment 标识，不泄漏 token；无 consent、不支持目的和 DMP timeout 的 fail-closed 测试。
- identity link 只发给匹配 partner；无 consent/过期/revoked 必须省略 `buyeruid`/EID；SSP 和 DMP revoke tombstone 后受众解析不得返回 segment。
- ClickHouse 同一 event 重放、快照未发布、发布前后切换、旧快照保留/过期查询测试。
- MariaDB 并发 lease、租约过期回收、同一 win event 重放和预算边界事务测试。

## 6. 参考规范

- [IAB Tech Lab OpenRTB 2.x / 2.6](https://github.com/InteractiveAdvertisingBureau/openrtb2.x)
- [Valkey persistence](https://valkey.io/topics/persistence/)
- [MariaDB Server documentation](https://mariadb.com/kb/en/documentation/)
- [ClickHouse MergeTree](https://clickhouse.com/docs/engines/table-engines/mergetree-family/mergetree)
- [ClickHouse data modeling](https://clickhouse.com/docs/data-modeling/)