# Open Ads 系统概要设计

状态：设计提案，尚未进入开发。本文用于评审边界、数据责任和关键风险；吞吐、地域、隐私法规和服务等级目标仍需业务确认。

## 1. 目标与约束

设计三个可独立构建、发布、扩缩容的 Go 应用：

- **SSP**：接入流量、执行竞价编排、对外返回 OpenRTB 响应。业务事实存储在 MariaDB 独立数据库 `ssp_db`，Valkey 仅缓存热数据。
- **DSP**：接收 OpenRTB BidRequest、做广告候选筛选和出价。业务事实存储在 MariaDB 独立数据库 `dsp_db`，Valkey 仅缓存热数据。
- **DMP**：管理受众数据和快照，提供受众解析及事件写入 API。业务数据存储使用 ClickHouse。

三个应用各自拥有领域模型、数据库所有权、迁移/发布流程和运行时配置。SSP 与 DSP 可部署在同一 MariaDB 集群，但必须使用不同数据库、账号、迁移流程和最小权限授权；不能跨库查询或共享表。DMP 独立使用 ClickHouse。Valkey keyspace 按应用隔离，只存可重建的缓存/短期缓冲数据。禁止共享领域实体、存储模型、Go `common` 业务包或跨库查询。服务间只通过有版本的 HTTP 契约交互。

开发语言为 Go；HTTP 使用标准库 `net/http`，业务层不依赖 Web 框架。数据库驱动和 OpenRTB wire DTO 属于基础设施依赖，不构成领域共享。

另提供独立静态 **Test Portal** 和轻量原生 JavaScript SDK 作为测试工具，不构成第四个广告业务 bounded context。SDK 浏览器 API 是 SSP 自有的版本化 client contract，不是 OpenRTB；SSP 将其映射为 OpenRTB 2.6 再请求 DSP。

## 2. 标准与边界

- SSP 与 DSP 的竞价请求/响应遵守 **OpenRTB 2.6**。SSP 是 exchange，DSP 是 bidder；在每个应用内独立实现 wire DTO 到本地领域对象的映射。
- SSP 到 DMP 的受众解析是内部 REST API，不属于 OpenRTB。DMP 返回 provider/segment 标识后，SSP 将其映射到 OpenRTB 2.6 的 `user.data[].segment[]`，再发送给 DSP。
- 私有字段只放在明确约定的 `ext` 中；不得修改标准字段含义，也不得要求 DSP 理解 DMP 的内部模型。
- 每个应用维护自己的 OpenRTB 2.6 契约测试和支持能力清单。具体媒体格式及扩展范围在需求评审后确定。
- Test Portal/JavaScript SDK 到 SSP 使用 `/sdk/v1` client API；该 API 只返回浏览器渲染所需的安全字段，禁止让浏览器直接访问 DSP、DMP 或 MariaDB。
- OpenRTB `user.id`、`buyeruid`、`eids` 按各自标准语义使用；不创建在三个应用间共享的全局 user ID。

## 3. 领域职责

| 应用 | 自己负责的业务事实 | 不负责 |
|---|---|---|
| SSP | Publisher、Property、Placement、SupplyEndpoint、AuctionPolicy、Deal、Auction、PublisherScopedSubject、PartnerIdentityLink、DeliveryLog、PublisherPayableLedger | DSP 广告主/预算事实、DMP 用户画像事实 |
| DSP | Advertiser、Campaign、LineItem、Creative、Targeting、Budget、BidDecision、DeliveryLog、AdvertiserSpendLedger | SSP 库存和拍卖事实、DMP 原始用户画像 |
| DMP | DataProvider、IdentityNamespace、ConsentEvent、SegmentDefinition、MembershipSnapshot、AudienceResolution | SSP/DSP 的账号、广告投放、竞价和预算事实 |

跨服务标识符在接收方是外部引用值，不是共享实体或数据库外键。每个 bounded context 自己校验外部引用并处理其不存在/过期情况。

## 4. 关键设计决定

1. **实时边界**：SSP 并发调用 DMP 与多个 DSP，并以 OpenRTB `tmax` 为总截止时间；DMP 查询和 DSP 调用各自有更短的可配置超时。预算耗尽时不等待完整超时，立即按配置降级。
2. **DMP 降级**：无标识、无授权、缓存未命中且 DMP 超时等情况下，不发送受众数据；SSP 是否继续竞价由 Placement 的隐私/受众策略决定，默认继续无受众竞价。任何策略都不得绕过 consent。
3. **SSP/DSP 热路径**：MariaDB 是各自业务事实源；配置变更经版本化刷新到 Valkey 和进程内只读快照。RTB 请求只读缓存/内存，不回源 MariaDB；缓存未预热或超过新鲜度上限时，实例不就绪或对受影响业务 no-bid。
4. **DSP 预算**：竞价决策不在每个 bid request 上读取 MariaDB；预算以后台预分配的有界额度租约供实例使用，billable event 与 advertiser spend 由 MariaDB 事务幂等结算，避免多实例各自超支。
5. **DMP 查询模型**：ClickHouse 保存追加式事件和不可变受众快照。先完整写入并校验快照，再追加发布标记；解析只读已发布版本，不依赖跨表事务或 `FINAL`。
6. **缓存职责**：Valkey 存 SSP/DSP 热配置快照、短期受众解析结果和有明确保留策略的短期缓冲数据；MariaDB 才是 SSP/DSP 持久业务事实源。
7. **日志与财务分离**：SSP、DSP 各自在自己的 MariaDB 保存配信事件日志与不可变结算分录。SSP 账本表达 publisher 应付，DSP 账本表达 advertiser 消耗；不跨库共享账务模型。
8. **计费语义**：OpenRTB `nurl` 是赢标通知，不代表已展示或可计费；DSP spend 以经验证的 `burl`/双方约定 billable event 入账。Publisher payable 由 SSP 的 publisher 合同计费策略计算。
9. **投放审计**：实时接口只保留必要的去敏事件字段。敏感标识使用受控的伪名化 token；原始 IP、完整 user ID、consent string 和完整 BidRequest 不写普通日志。
10. **测试入口**：门户使用测试租户/fixture 和短期、受限的 SDK token；线上长期密钥、数据库凭证和 DSP endpoint secret 不进入 HTML、JavaScript bundle 或浏览器存储。
11. **身份隔离**：SSP 是 exchange identity mapping 的拥有者；为每个 publisher/partner 建 pairwise alias。DMP 只用 tenant/namespace-scoped HMAC token 查受众，DSP 只接收自身 `buyeruid` 和明确授权的 `eids`；无同意/无映射时不传身份，不做 fingerprinting。

## 5. 组件视图

```mermaid
flowchart LR
  Portal[Test Portal static page] --> SDK[Vanilla JS SDK]
  SDK -->|SSP Client API /sdk/v1| SSP[SSP Go App]
  Supply[流量来源 / Publisher] -->|OpenRTB 2.6| SSP[SSP Go App]
  SSP -->|内部 REST: 受众解析| DMP[DMP Go App]
  SSP -->|OpenRTB 2.6 BidRequest| DSP[DSP Go App]
  DSP -->|OpenRTB 2.6 BidResponse| SSP
  SSP -->|OpenRTB Win notice callback| DSP
  SSP -->|批量事件 REST / 重试| DMP
  SSP --- SSPDB[(MariaDB / ssp_db)]
  DSP --- DSPDB[(MariaDB / dsp_db)]
  SSP -. 热缓存 .-> VK[(Valkey)]
  DSP -. 热缓存 .-> VK
  DMP --- CH[(ClickHouse)]
  SSP -->|OpenRTB 2.6 响应| Supply
```

## 6. 设计验收门槛

开发前必须确认：峰值 QPS、请求体大小、调用地域/网络拓扑、OpenRTB 媒体格式、拍卖类型、预算精度与超支容忍度、publisher payable 与 advertiser billable 的业务依据、缓存最大陈旧时间、DMP 数据新鲜度、隐私法规和数据保留期限。随后用目标规模压测验证 SSP 总延迟分配、Valkey 缓存命中/内存、MariaDB 控制面/账务写入与后台预算租约、ClickHouse 受众查询 P95/P99；另验证日志积压、重复计费防护、缓存冷启动和 MariaDB 故障期间的行为。

特别注意：ClickHouse 面向分析和批量写入，不是传统 OLTP。若 DMP 逐用户查询的吞吐/尾延迟无法通过快照布局、SSP 短 TTL 缓存和压测目标，需在开发前重新评估“DMP 仅使用 ClickHouse”这一约束，不能通过掩盖查询延迟来宣称满足在线服务等级。

详细内容见 [HLD](HLD.md)、[LLD](LLD.md) 与 [API 设计](API.md)。