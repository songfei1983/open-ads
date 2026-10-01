# Open Ads 低层设计（LLD）

本文定义建议的聚合、端口、REST 契约、存储布局和失败语义。数据库版本、驱动、部署拓扑和规模指标待评审后锁定。

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
| Property | site/app 元数据、所属 publisher | 外部引用 publisher ID，不复制 Publisher 聚合 |
| Placement | 格式/尺寸、floor、currency、deal 和隐私/降级策略 | 格式约束合法；隐私策略不能被请求参数覆盖 |
| SupplyEndpoint | 入口身份、限流和协议配置 | 仅接受明确支持的 OpenRTB 版本/profile |
| AuctionPolicy | `at`、币种、出价校验、floor 和 tie-break 规则 | 结果必须符合 request 和 placement policy |
| Auction | 单次 request 的 deadline、有效候选和结算结果 | request/imp/bid 关联正确；过期候选不参与选择 |

Auction 是短生命周期对象，默认只存在于请求上下文中；持久审计写去敏 outcome event，不持久化完整用户请求。

### 2.2 OpenRTB 2.6 入口与 DSP 出站

- Supply 入口建议 `POST /openrtb2/auction`；对外名称可按供给方接入 profile 配置。
- DSP adapter 建议对每个 DSP 配置 `POST /openrtb2/bid` endpoint。发送/接收标准 OpenRTB 2.6 JSON，并按对端 profile 设置 `Content-Type`、`Accept` 和版本标记。
- 验证 `BidRequest.id` 非空、`imp` 非空且各 `imp.id` 唯一；只处理已声明支持的 impression 格式。校验响应 `BidResponse.id` 与 request id 相等，`seatbid[].bid[].impid` 必须指向原始 imp，出价币种、floor、尺寸/格式及 deadline 必须满足约定。
- 处理 no-bid 按 OpenRTB 2.x 语义实现，可包含 `seatbid: []`/`nbr` 等适用形式；DSP 错误、超时或无有效 bid 均不构成候选。
- 拍卖的 `at`、币种、price macro 展开和 winner notice 规则按 OpenRTB 2.6 与供给方约定实现。内部货币以整数微单位表达，wire 上遵从标准价格字段，不用浮点数做内部累计。
- 不支持的 `ext` 忽略或按 profile 拒绝；不把私有字段放入标准字段。

### 2.3 DMP 受众解析契约（内部 API，不是 OpenRTB）

```http
POST /internal/v1/audience:resolve
Content-Type: application/json
Authorization: service identity
```

请求字段：`request_id`、`tenant_id`、`provider_id`（可选）、`namespace`、`subject_token`、`consent_context`、`purposes`、`max_segments`。`subject_token` 是按 namespace 生成的伪名化标识，不传原始邮箱、电话或原始设备 ID。无合法处理依据时 SSP 不调用此 API。

响应字段：`request_id`、`snapshot_id`、`provider_id`、`segments: [{id}]`、`resolved_at`。返回值按 `max_segments` 截断，segments 的 `id`/`name` 可映射到 OpenRTB `user.data[].segment[]`。DMP 不向 SSP 暴露 profile 细节、分数或 ClickHouse 字段。

错误语义：400 表示契约无效；401/403 表示服务身份/权限失败；429 表示限流；503 表示暂不可用；超时由调用方截止时间控制。合法请求但无匹配受众返回空 segments，不返回 404。

### 2.4 Valkey 数据布局

配置快照使用 JSON 字符串或紧凑字段编码；不要求 ValkeyJSON 模块。所有 key 采用版本化前缀和租户 hash tag，示例：

```text
ssp:v1:{tenant}:publisher:<publisher_id>       -> Publisher aggregate
ssp:v1:{tenant}:property:<property_id>          -> Property aggregate
ssp:v1:{tenant}:placement:<placement_id>        -> Placement aggregate
ssp:v1:{tenant}:idx:publisher-properties        -> Set<PropertyID>
ssp:v1:{tenant}:idx:property-placements         -> Set<PlacementID>
ssp:v1:{tenant}:aud:<namespace>:<token-hash>    -> cached segment IDs, short TTL
ssp:v1:{tenant}:outbox                           -> Stream of immutable outcome events
```

- 聚合根 key 是权威配置；索引可从聚合快照重建。更新脚本只对同一 `{tenant}` hash slot 的 key 操作，校验 aggregate version 后原子写快照/索引或 outbox。
- 受众缓存只存最小 segment ID、snapshot ID 和过期时间；TTL 可配置并受隐私策略限制。缓存 key 使用 HMAC/token hash，不能直接使用可识别 ID。
- 竞价请求临时状态保留在进程内并受并发上限约束。Valkey Stream outbox 使用 consumer group、ack、重试计数和保留上限；确认写入 outbox 后再返回结果，需压测其对 `tmax` 的影响。
- 推荐 AOF + 定期 RDB；恢复目标和磁盘容量需压测确认。Valkey 数据必须备份/恢复演练，不能把“内存数据库”误认为自动持久化。

## 3. DSP LLD

### 3.1 聚合与领域规则

| 聚合根 | 主要内容 | 关键不变量 |
|---|---|---|
| Advertiser | 广告主状态、允许币种 | 暂停广告主下 line item 不出价 |
| Campaign | 活动状态、投放周期、投放目标 | 活动时间窗和总预算有效 |
| LineItem | 预算、bid strategy、targeting、频次/日限额 | 只有有效且预算租约充足时可候选 |
| Creative | 素材格式、落地页、审核状态 | 素材须匹配 impression 格式和审核规则 |
| BudgetAccount | 已结算、预留、可用金额及版本 | 可用金额不小于零；重复 win event 不重复扣减 |
| WinSettlement | 唯一通知 ID、auction/bid/imp、金额 | 同一通知的重复回调为幂等成功 |

Targeting DSL 属 DSP 私有领域语言；仅从 OpenRTB request 读取标准上下文，不将 DMP segment 解释为 DMP 实体，只按 `data.id`/`segment.id` 配置匹配。

### 3.2 MariaDB 表草案

全部业务表使用 InnoDB、明确主键、UTC 时间和外键/唯一约束；tenant 条件需进入唯一键与查询索引。

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
  settled_micros BIGINT UNSIGNED NOT NULL,
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

CREATE TABLE win_settlement (
  tenant_id BINARY(16) NOT NULL,
  event_id BINARY(32) NOT NULL,
  auction_id VARCHAR(128) NOT NULL,
  bid_id VARCHAR(128) NOT NULL,
  imp_id VARCHAR(128) NOT NULL,
  line_item_id BINARY(16) NOT NULL,
  currency CHAR(3) NOT NULL,
  price_micros BIGINT UNSIGNED NOT NULL,
  received_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, event_id),
  KEY idx_settlement_line_time (tenant_id, line_item_id, received_at)
) ENGINE=InnoDB;
```

上面是概念 schema；字符集、外键级联策略、campaign/line item 的生命周期删除方式和价格单位需迁移设计评审，不直接视为可执行最终迁移。

### 3.3 预算并发与竞价

1. 配置变更由 MariaDB 事务提交并增加版本；DSP 节点通过轮询变更版本/管理 API 获取完整一致快照，校验后原子替换本地只读快照。
2. 节点出价只使用尚未过期的本地快照和预算 lease。lease 由 MariaDB 条件更新/事务分配，实例不得使用超过剩余额度的租约金额。
3. 收到 win notice 后，以稳定 `event_id` 在一个 InnoDB 事务中插入 `win_settlement` 并更新 settled/lease 金额。唯一键冲突表示重放，返回幂等成功；事务失败则重试。
4. lease 到期回收需检查 owner 与剩余金额，使用锁/条件版本避免与结算竞争。实例失联会造成额度暂时不可用而非超支。
5. 出价热路径不得等待慢 SQL。快照过旧、lease 不可用或预算状态不确定时，该 line item no-bid。

### 3.4 DSP HTTP 契约

- `POST /openrtb2/bid`：OpenRTB 2.6 BidRequest，返回标准 BidResponse 或标准 no-bid。解析/评估受请求 deadline 限制。
- `GET /healthz`、`GET /readyz`：仅报告进程健康和关键依赖/快照准备度，不暴露业务数据。
- BidResponse 的 `nurl` 指向 DSP 自己的赢标通知端点，严格按 OpenRTB 2.6 定义展开受支持宏；通知处理幂等且快速返回。
- Campaign CRUD/审核管理 API 不属于 bidder endpoint，可作为后续独立控制面需求，不在第一期默认范围。

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

受众查询先解析当前已发布 snapshot，然后限定 `tenant_id + snapshot_id + namespace + subject_token` 做精确匹配并限制 segment 数量。API 应将 snapshot id 缓存在进程内短时间；该缓存到期后通过 publication 表刷新。上线前必须用最大分区和生产级基数做 P95/P99/并发压测。

事件至少一次：相同 `event_id` 可因重试重复写入；分析查询必须采用 `uniqExact(event_id)` 或明确的去重层。不得假设 MergeTree 会提供唯一约束或同步去重；物化视图不能被当作源表去重保障。

### 4.3 DMP HTTP 契约

| Endpoint | 用途 | 写入/响应规则 |
|---|---|---|
| `POST /internal/v1/audience:resolve` | 实时查询某 token 的 segment IDs | 限制结果数；无结果返回空集合；只查已发布快照 |
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
- 请求 `id`、imp ID、`tmax`、`at`、currency、`user.data[].segment[]` 的映射与边界测试。
- DSP 响应 request ID 不匹配、未知 imp、非法价格/币种、超时、空 seatbid 和无效 creative 的拒绝/no-bid 测试。
- DMP segment 映射保留 provider/segment 标识，不泄漏 token；无 consent、不支持目的和 DMP timeout 的 fail-closed 测试。
- ClickHouse 同一 event 重放、快照未发布、发布前后切换、旧快照保留/过期查询测试。
- MariaDB 并发 lease、租约过期回收、同一 win event 重放和预算边界事务测试。

## 6. 参考规范

- [IAB Tech Lab OpenRTB 2.x / 2.6](https://github.com/InteractiveAdvertisingBureau/openrtb2.x)
- [Valkey persistence](https://valkey.io/topics/persistence/)
- [MariaDB Server documentation](https://mariadb.com/kb/en/documentation/)
- [ClickHouse MergeTree](https://clickhouse.com/docs/engines/table-engines/mergetree-family/mergetree)
- [ClickHouse data modeling](https://clickhouse.com/docs/data-modeling/)