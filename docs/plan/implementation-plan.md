# Open Ads 首期开发计划

状态：实施规划草案，待跨职能评审确认后执行。本文基于 [系统概要设计](../design/README.md)、[HLD](../design/HLD.md)、[LLD](../design/LLD.md) 和 [API 契约](../design/API.md) 拆解工作，不代表设计提案已批准或生产上线授权。

## 1. 目标与计划假设

首期交付一个可隔离验收的 sandbox 闭环：管理员配置供给与投放资源；Supply 或 Test Portal/JS SDK 发起请求；SSP 编排 OpenRTB 2.6 竞价；DSP 按已发布快照、targeting 和预算决策；SSP 安全地返回静态图片广告；有效 impression 产生幂等的 publisher payable 与 advertiser spend 结算链路。DMP 的快照解析和撤回屏障可独立验收，首期 segment 只从 SSP 透传到 DSP，不影响 DSP 决策。

计划按**两周一个迭代**估算，共 8 个迭代（含实施前迭代）。这是容量规划假设，不是对日历日期或团队人数的承诺，需在确认团队容量后校准。SSP、DSP、DMP 可以在公共底座就绪后并行开发，但每个应用保持独立 Go module、领域模型、数据库账号和发布流程；不可通过共享领域包或跨库查询来缩短工期。

本计划不包含生产流量、真实付款/开票、video/native、Deal、频控、pacing、segment targeting，也不安排适用法规评审（依照本轮范围决定）。生产启用须另行评审和授权。

## 2. 开发前必须关闭的决策

Iteration 0 的退出条件是以下事项均有记录、有负责人和批准结论。若结论影响契约、数据模型、财务或安全不变量，不得仅以临时默认值进入实现。

| ID | 待确认事项 | 建议负责角色 | 关闭方式/阻塞范围 |
|---|---|---|---|
| D-01 | 部署地域/拓扑、网络边界、服务发现、TLS/mTLS 与密钥分发 | 架构/运维 | 形成部署视图；阻塞环境、安全和容量测试 |
| D-02 | SSP 请求体最大尺寸、超时预算分配、各 API 限流和响应大小 | 产品/架构/研发 | 写入 API 与配置基线；阻塞入口实现及压测 |
| D-03 | OpenRTB 2.6 首期 profile：必填字段、支持能力、宏、notice 方法/超时/重试 | 产品/研发/测试 | 固化带版本 JSON fixtures 与兼容性测试 |
| D-04 | DSP overspend 容忍度、budget rounding/settlement 规则、reservation 最大有效窗口/grace | 产品/财务/研发 | 明确边界及并发/迟到事件策略；阻塞预算与结算实现 |
| D-05 | Publisher payable 与 advertiser billable 的业务依据、舍入时点、费用差异表达 | 产品/财务 | 书面确认 Sandbox 计费 profile；不启用真实结算 |
| D-06 | 未列明 delivery event、账本、诊断数据的技术保留/删除规则 | 产品/数据/运维 | 定义数据分类、起算点和可验证删除方式 |
| D-07 | 撤回完成 SLA、DMP 数据新鲜度、SSP/DMP 缓存最大陈旧时间 | 产品/数据/研发 | 定义 SLO 与 fail-closed/降级语义 |
| D-08 | 分析事件允许丢失目标、最大积压、保留期与告警阈值 | 产品/数据/运维 | 为有界缓冲配置 loss budget 和演练方案 |
| D-09 | 生产供给授权/透明度的未来责任边界 | 产品/供给/安全 | 确认 Sandbox 是否仅使用测试 Supply；生产项列入后续决策 |
| D-10 | ClickHouse 精确 subject 查询及撤回物理删除能否达到目标拓扑下的服务目标 | 数据/架构/运维 | 建立代表性数据 POC；不达标时在继续 DMP 在线实现前调整设计 |

已确认并沿用的基线：sandbox-only；SSP 峰值目标 3,000 QPS；广告请求端到端 P95/P99 均不超过 100 ms；MariaDB 最低 10.6；主体/身份映射和 membership snapshot 最长保留 3 个月、DMP 分析事件 12 个月、主存储删除不晚于触发后 30 天、备份 90 天滚动到期。期限起算点与撤回 tombstone 按设计文档执行。具体测量拓扑和运行窗口在 D-01/D-07 中确认；不得把压测目标当作已达到的事实。

## 3. 迭代计划

### Iteration 0 — 需求/契约封板与技术验证（2 周）

**目标：** 跨职能确认首期边界，关闭 D-01 至 D-10；把设计提案转成可追踪的实现基线。

| 任务 | 工作项与交付物 | 完成条件 |
|---|---|---|
| I0-01 | 产品、设计、研发、测试、运维与财务走查本计划和 HLD/LLD/API；记录决策、风险、未决项负责人 | 会议结论和 D-01…D-10 决策记录获相关负责人确认；无未分配的阻塞项 |
| I0-02 | 固化 OpenRTB/client/admin/internal API 的版本化 fixtures、错误语义、状态转换和示例 | SSP/DSP/DMP 负责人对契约 diff 签字；同一行为没有 README/HLD/LLD/API 多套冲突定义 |
| I0-03 | 用目标数据量验证 ClickHouse subject lookup、snapshot 发布切换、撤回 tombstone 查询与物理删除方案 | 测试报告包含数据规模、查询计划/延迟、mutation 或 shadow rebuild 开销、磁盘峰值和失败恢复结论；D-10 有明确 Go/No-Go |
| I0-04 | 建立威胁边界与测试数据方案：tenant 隔离、mTLS 身份、SSRF/Origin、AEAD token、敏感日志检查 | 安全与测试评审确认测试清单；仅使用合成身份和测试租户 |
| I0-05 | 将架构验收要求映射到 Epic、任务、接口 owner、测试 owner 和发布门禁 | 每项架构门槛均能追踪到任务及测试，不以“开发完成”代替验收 |
| I0-06 | 产品/设计/研发/测试共同走查静态 Portal 与 SSP/DSP/DMP 控制台原型 | 核准首期页面、角色和操作范围；记录交互差异与待补设计；明确原型只是演示而非服务实现 |

**退出门槛：** D-01…D-10 已关闭或明确标成不阻塞的后续项；D-10 未通过则暂停 DMP 在线 lookup 开发；预算/财务语义未确认则不得实现可写结算。

### Iteration 1 — 工程底座与可运行骨架（2 周）

**目标：** 建立三个独立服务和可重复的本地/CI 验证环境。

| 任务 | 工作项与交付物 | 完成条件 |
|---|---|---|
| I1-01 | SSP、DSP、DMP 独立 Go module、启动入口、配置加载、优雅关闭 | 每个应用可独立构建、运行、配置和发布；无共享领域 package |
| I1-02 | `net/http` 路由骨架、中间件、request ID、deadline、结构化日志、metrics 接口、`/healthz` 与 `/readyz` | 探针状态符合就绪条件；日志不包含 token、原始身份或完整竞价 payload |
| I1-03 | MariaDB 10.6 `ssp_db` / `dsp_db`、ClickHouse、Valkey 的本地/CI 集成环境与隔离账号 | 最小权限可验证；SSP/DSP 无跨库权限；Valkey ACL/keyspace 相互隔离 |
| I1-04 | 三服务各自迁移/回滚机制、CI（format、vet、unit、migration validation） | 空库可从头迁移；重复执行/失败迁移有可预测结果；流水线可阻断失败 |
| I1-05 | ID、金额微单位、时钟、配置版本和测试 fixture 的应用内基础组件 | UUID v7、全小写 32-hex wire 表示及 signed `*_micros` 约束有自动化测试 |
| I1-06 | 监控/告警仪表盘初版与 sandbox 环境部署脚本 | 能关联 request ID、配置版本、异常、延迟和队列积压；不展示敏感值 |

**退出门槛：** 三服务可分别构建和部署；数据库隔离与迁移验证通过；持续集成与健康探针工作。

### Iteration 2 — 基础 RTB 协议与请求流（2 周）

**目标：** 先打通 SSP ingress 到 DSP bidder 的最小、可测量竞价骨架，确立 OpenRTB 与 no-bid 的服务边界。

| 任务 | 工作项与交付物 | 完成条件 |
|---|---|---|
| I2-01 | SSP/DSP OpenRTB 2.6 wire DTO、映射和版本化标准 fixtures | 双方独立 round-trip；不共享领域类型；请求/响应 ID、imp、currency 边界明确 |
| I2-02 | SSP OpenRTB ingress 基础校验、request ID、deadline、大小限制与 no-bid 响应 | 非法请求按契约拒绝；no-bid 与 transport error 可区分；请求不会超过配置 deadline |
| I2-03 | DSP bidder handler 骨架、健康/就绪探针和可替换决策端口 | 能响应有效测试请求及标准 no-bid；无 MariaDB 读取；日志不输出完整 BidRequest |
| I2-04 | SSP→DSP 单 bidder 客户端、超时/cancel/连接池和错误映射 | 超时、取消、错误响应可观测且按 no-bid 处理；request ID 不匹配的响应被拒绝 |
| I2-05 | 固定协议 fixture 驱动的 SSP↔DSP 集成测试与本地 smoke 命令 | CI 可启动两服务并完成一次 bid/no-bid 往返；测试结果可复现 |

**退出门槛：** SSP/DSP 协议往返和 no-bid/timeout 测试通过；形成后续 fan-out 和配置决策使用的稳定契约基线。

### Iteration 3 — DSP 控制面与决策热路径（2 周）

**目标：** DSP 可发布 Campaign/Line Item/Creative 快照，并在不访问 MariaDB 的热路径上完成首期决策。

| 任务 | 工作项与交付物 | 完成条件 |
|---|---|---|
| I3-01 | Advertiser/Campaign/Line Item/Creative 管理 API、version 乐观锁和发布校验 | 未发布配置不影响 bidder；不支持的 targeting 条件（包括 segments）返回约定错误 |
| I3-02 | 静态素材上传、文件签名/真实格式解码、尺寸/hash 校验、不可变 `asset_ref` | 伪造 MIME、损坏、超限、哈希/尺寸不符均拒绝；存储凭据不外泄 |
| I3-03 | DSP 快照发布/刷新、实例版本报告、Valkey 故障与 readiness 语义 | cache miss 不回源 MariaDB；冷启动未预热不 ready；陈旧实例按门槛处理 |
| I3-04 | OpenRTB 2.6 bidder、字段校验、domain/page/banner targeting 与 bid/no-bid | 所有标准 fixtures round-trip；匹配算法符合精确匹配、路径前缀边界和维度组合规则 |
| I3-05 | Daily/lifetime BudgetAccount、lease allocator、reservation 原子扣留与失效释放 | 多 scope reservation 全有或全无；不可用租额时不出价；UTC rollover 与额度上限正确 |
| I3-06 | DSP health/ready、候选/no-bid/超时/快照指标 | 指标不含身份或 token；DSP 超时、非法请求与 no-bid 语义可区分 |
| I3-07 | DSP 管理台：Campaign、Line Item、Creative、预算及隔离项查询/处置页面 | 页面只通过 DSP 管理 API；发布确认、字段校验、权限边界和错误反馈可浏览器验证 |

**退出门槛：** OpenRTB 基础出价与预算保留通过独立集成测试；热路径无 SQL 访问证据；已发布配置版本可追踪。

### Iteration 4 — DMP 独立子系统与隐私门禁（2 周）

**目标：** 按风险优先级完成可独立验收的 Data Provider、Segment、Snapshot、解析与 fail-closed 撤回屏障。

| 任务 | 工作项与交付物 | 完成条件 |
|---|---|---|
| I4-01 | Provider/namespace/segment 定义与版本化管理 API | tenant 授权、字段校验、乐观锁和审计记录通过接口测试 |
| I4-02 | Snapshot 导入任务、受信 tokenization adapter 输入、批量写入与完整性核验 | 拒绝原始身份输入；行数/checksum/重复率核验失败不替换旧版本；重跑幂等 |
| I4-03 | Snapshot 不可变发布指针与 Audience Resolve 精确键查询 | 未发布 snapshot 不可解析；固定 snapshot ID 查询；结果数有上限且不回显 token |
| I4-04 | Valkey deny gate、epoch、in-flight admission/finalize、ClickHouse tombstone 与状态 API | 撤回阻止新 admission；在途响应再次校验；不确定状态 fail-closed |
| I4-05 | Valkey epoch 丢失重建、ClickHouse 故障、节点重启和撤回恢复演练 | gate 重建一致前不 ready；恢复期间无越权受众响应 |
| I4-06 | Snapshot/事件保留、撤回物理删除及备份到期自动化 | 符合已批准期限；单 snapshot 删除不误删共享月分区中的其他数据 |
| I4-07 | DMP 服务独立集成、负载与物理删除 POC 复测 | D-10 通过；解析及删除目标在已确认拓扑和数据规模下达标 |
| I4-08 | DMP 管理台：Provider、Segment、Snapshot 导入/校验/发布、撤回状态页面 | 导入校验状态清晰；发布需显式确认；撤回 UI 不接收或显示原始身份/token |

**退出门槛：** DMP 在隔离环境独立通过发布、解析、撤回和恢复测试；主存储删除与备份生命周期有自动化证据。

### Iteration 5 — SSP 控制面与端到端请求/渲染（2 周）

**目标：** Supply 与 Portal/SDK 均能经过 SSP/DSP 完成受控的图片广告闭环；不在本迭代开放真实账务。

| 任务 | 工作项与交付物 | 完成条件 |
|---|---|---|
| I5-01 | Publisher/Property/Placement/AuctionPolicy/Supply/DSP endpoint 管理 API 和版本化配置发布 | 发布校验关联资源、币种、floor 和 `at=1`；配置通过 outbox/刷新器生效 |
| I5-02 | Supply OpenRTB ingress、供给身份/tenant 绑定、大小/Origin/限流/deadline | 不从请求体推断 tenant；无效凭据、超限及超时有安全明确响应 |
| I5-03 | SSP 并行 DSP fan-out、候选校验、first-price 胜者选择和 no-bid | 每个 DSP deadline/并发隔离；超时不拖累其他 bidder；非法 response 不成为胜者 |
| I5-04 | SSP Client API v1 与原生 JS SDK；Portal filled/no-fill/error 和脱敏诊断 | Portal smoke 完成请求及状态展示；协议和错误状态与 fixtures 一致 |
| I5-05 | DSP→SSP creative registry 同步、白名单校验和受控静态图片 renderer | 只渲染已发布静态素材；不执行任意 HTML/JS；iframe sandbox/CSP/destroy/过期 URL 测试通过 |
| I5-06 | SSP/DMP adapter、撤回前后检查与 segment 透明透传 | consent/撤回/未知状态 fail-closed；DMP timeout 遵循确认策略；DSP 决策不读取 segments |
| I5-07 | SSP 配置刷新、跨实例版本状态、陈旧实例摘除及管理台状态 | manifest、实例加载、失败 outbox 可查、可重试并告警 |
| I5-08 | SSP 管理台：Publisher、Placement、Supply、DSP endpoint、配置发布与运行状态页面 | 页面只通过 SSP 管理 API；未发布配置不会显示为生效；发布/重试结果和实例版本可解释 |

**退出门槛：** 测试 Supply 和 Portal/SDK 均可完成 filled/no-fill/error；未授权素材和敏感值不进入浏览器；配置仅在发布后生效。

### Iteration 6 — Impression、账本与可靠通知闭环（2 周）

**目标：** 完成 impression 唯一账务链路、reservation 终态、outbox 重试/隔离与冲正。

| 任务 | 工作项与交付物 | 完成条件 |
|---|---|---|
| I6-01 | AEAD event token 签发/解封、用途/audience/期限/密钥版本校验 | 篡改、过期、未知版本、跨用途/audience token 均拒绝；日志无 token |
| I6-02 | Impression tracker、唯一 event key；Supply/SDK audience 汇入同一账务幂等事务 | 合法 impression 最多计费一次；伪造/重放/click/render error 不产生财务分录 |
| I6-03 | SSP payable ledger、delivery event 与持久 billable outbox 原子提交 | DB 故障全部回滚；Valkey 清空不影响从 token 恢复财务上下文 |
| I6-04 | DSP `burl` 校验、Advertiser spend ledger、reservation 终态与预算范围更新 | 同 event 同内容幂等；同 ID 不同 body 冲突隔离；事务提交后才成功响应 |
| I6-05 | `nurl`/`lurl`/`burl` 互斥、grace、迟到/冲突 quarantine 和处置 API | nurl 不结算；loss/billable 互斥；过 grace 项不静默丢弃、不重复释放预算 |
| I6-06 | Publisher/DSP ledger reversal 与关联审计 | 部分/全额冲正仅追加；并发、超额、币种/方向错误回滚 |
| I6-07 | Outbox allowlist、退避重试、4xx 隔离及 429/5xx/网络故障恢复 | SSRF/重定向绕过被拒绝；待投递、隔离、重放可观测 |

**退出门槛：** impression 至 advertiser spend 全链路通过故障注入、重放和并发测试；账本可对账；非账务 telemetry 不挤占账务容量。

### Iteration 7 — 系统级验证与 Sandbox 发布候选（2 周）

**目标：** 在已确认拓扑验证 SLO、安全、可靠性和运维恢复，交付可演示、可回滚的 sandbox release candidate。

| 任务 | 工作项与交付物 | 完成条件 |
|---|---|---|
| I7-01 | SSP 入口 3,000 QPS 目标负载下端到端压测和延迟预算分析 | 报告可复现；规定窗口内 P95/P99 均 ≤100 ms；列明资源、错误率、缓存命中和瓶颈 |
| I7-02 | DSP fan-out、DMP lookup、数据库/缓存/ClickHouse 并发及积压容量测试 | 按入口目标负载派生；记录组件分位数、队列上限、资源水位和退化行为 |
| I7-03 | 故障恢复演练：MariaDB、Valkey、ClickHouse、DSP、outbox、冷启动/缓存丢失 | 符合 fail-closed/no-bid/重试/readyz 约束；不丢账务事实、不绕过撤回 gate |
| I7-04 | 安全/隐私验收：tenant 隔离、mTLS、Origin、SSRF、token、日志、删除和备份恢复 | 无未处置高风险项；敏感数据扫描及删除证明可审阅 |
| I7-05 | 对账、账务隔离、值班告警/runbook、迁移和回滚演练 | 账务差异可识别/隔离；回滚不破坏追加账本；值班人员可定位并安全重试 |
| I7-06 | 产品验收会：Portal、Supply、控制台和 DMP 隐私流程；归档遗留问题 | 产品/设计/研发/测试确认结果；阻塞缺陷关闭，其余问题有 owner/目标版本 |

**退出门槛：** 发布门禁通过、产品验收签字，限定在 sandbox 测试租户；不代表生产流量或真实结算获批。

**退出门槛：** 所有发布门禁通过，产品验收签字，明确限定为 sandbox 测试租户；不代表生产流量或真实结算获批。

## 4. 跨服务依赖和并行工作

| 工作流 | 主要内容 | 依赖 |
|---|---|---|
| 基线与平台 | I0 决策、I1 CI/运行环境、部署/密钥/监控 | 无；是其他所有服务的前置 |
| RTB 基础 | OpenRTB DTO、SSP ingress、DSP bidder 和 no-bid | I0 的 D-02/D-03、I1 公共底座 |
| DSP | 管理配置、素材、bidder、targeting、budget lease | I2 契约稳定；I0 的 D-04；结算写路径等待 D-05 |
| DMP | Snapshot、Resolve、deny gate、删除恢复 | I0 的 D-07/D-10、I1 ClickHouse/Valkey 环境；DMP POC 在 I0 完成 |
| SSP 集成 | 管理配置、Supply ingress、auction、SDK/renderer、DMP adapter | I2/I3 契约；可与 DMP 管理面并行，端到端集成在 I5 完成 |
| 财务闭环 | Impression、ledger、notice、reservation、outbox | I3 budget model、I5 request/render、I0 的 D-04/D-05 |
| 质量与发布 | 契约/集成/安全/压测/故障演练和产品验收 | 每迭代持续参与；I7 集中完成发布门禁 |

推荐由不同 owner 并行负责 SSP、DSP、DMP 与平台/质量工作流。跨服务集成先用固定契约 fixtures 和测试替身，不让一个服务直接依赖另一个服务的领域模型或数据库。

## 5. 验收标准

以下标准作为 Sandbox release candidate 的统一验收清单；实现前需将依赖决策 D-01…D-10 的具体数值和环境写入测试计划。

### 5.1 产品功能

- 已发布 Placement、Campaign、Line Item、Creative 版本才影响运行时；未发布/校验失败配置不改变当前快照。
- 测试 Supply 与 Portal/SDK 均能获得 filled、no-fill 和明确错误；no-fill 不伪装成 transport failure。
- 竞价仅使用单一 image banner、单币种、OpenRTB `at=1` first-price；targeting 严格限定为 domain 精确、page 路径前缀、banner 尺寸精确匹配和预算。
- DMP 的 snapshot 只有在行数/checksum 完整性验证通过后才可发布/解析；解析只返回最小 segment ID；segment 仅透传、不参与 DSP 决策。
- Renderer 只渲染已发布白名单静态素材；不执行 bidder 提供的任意 HTML/JavaScript。
- 有效 impression 最多生成一条 payable/spend 链路；click、render error、普通测量事件不生成财务分录。
- 撤回完成后，所有相关在线 gate/key version 生效；并发/迟到解析不得把受撤回主体或 segment 发给 DSP。
- 页面/日志/指标/错误不包含原始主体身份、可逆 token、完整 BidRequest 或对象存储凭据。

### 5.2 技术与非功能

- SSP 在确认的测试拓扑和负载窗口下承受 3,000 QPS，端到端 P95、P99 均 ≤100 ms；同时报告错误率、no-bid、各服务延迟和资源使用。
- DSP bid 热路径无同步 MariaDB 读取；缓存 miss、节点冷启动、配置陈旧按设计 no-bid/not-ready，不通过未受控 DB 回源绕过。
- MariaDB 事实、账本和可靠 outbox 使用事务与幂等键；重复请求/notice 不重复创建财务事实。
- 预算多 scope reservation 全有或全无，no-bid/失效/lease 到期不会超过已确认的 overspend 界限；daily scope 在 `00:00 UTC` 正确 rollover。
- ClickHouse snapshot 查询固定不可变 `snapshot_id`；撤回/到期关闭 admission 立即生效，主存储在确定 SLA 内完成删除，备份按确定策略滚动到期。
- Valkey 丢失或 epoch 不一致时可从权威持久记录恢复；恢复完成前撤回解析 fail-closed，账本不得依赖 Valkey 恢复。
- 服务 readyz、发布版本、outbox/telemetry 积压、预算差异和撤回状态有可用指标及告警；部署/迁移可回滚。

## 6. 验收与测试方案

### 6.1 测试层次与执行门禁

| 层次 | 执行内容 | 通过要求 |
|---|---|---|
| 单元/领域测试 | 状态机、价格/预算计算、targeting、token 解析、错误映射 | 所有新增业务规则和边界有确定性测试；无 flaky；`go test ./...` 通过 |
| 契约/兼容测试 | OpenRTB 2.6、Client API v1、Admin/Internal API fixtures、错误码和版本 | SSP/DSP/DMP 各自独立验证；序列化 round-trip 和向后兼容检查通过 |
| 存储集成测试 | MariaDB 10.6 事务/锁/迁移，ClickHouse snapshot/tombstone，Valkey Lua/epoch/ACL | 使用与目标版本匹配的服务；检查真实唯一约束、回滚、并发和恢复行为 |
| 服务集成测试 | 服务替身及真实服务组合；DSP notice、DMP Resolve、creative registry、outbox | 网络失败、deadline、重复、乱序和服务部分不可用行为与契约相符 |
| 端到端/浏览器测试 | Portal/SDK 请求、广告渲染、no-fill/错误诊断、SSP/DSP/DMP 控制台操作 | 桌面及窄屏 smoke 通过；控制台无未处理异常；不显示敏感字段，键盘焦点和可访问名称可用 |
| 安全/隐私测试 | tenant 跨界、mTLS 身份、Origin、SSRF、AEAD、日志、撤回/删除 | 拒绝路径 fail-closed；无敏感数据泄漏；高风险发现阻断发布 |
| 压力/稳定性测试 | 3,000 QPS 入口及派生 DSP/DMP/存储负载、冷缓存和积压 | 按确认拓扑达到 P95/P99 门槛；队列/内存/磁盘不越上限；退化行为满足约定 |
| 恢复/演练 | Valkey/DB/ClickHouse 故障、备份恢复、重放、回滚和 quarantine | 账务事实不丢失、不重复；撤回 gate 重建前不恢复解析；runbook 可执行 |

每次提交必须通过 format、静态检查、unit 和受影响 module 的契约测试。每个迭代至少运行受影响服务的存储集成测试；进入系统测试前运行完整服务集成和浏览器 E2E。压测、安全验收及恢复演练作为 release gate，不用单测结果替代。

### 6.2 关键验收场景

| ID | 场景/步骤 | 预期结果 |
|---|---|---|
| AT-01 | 发布一组有效 Supply、Placement、Campaign、Line Item、Creative；发起标准 OpenRTB 请求 | 只用已发布版本，返回符合 OpenRTB 2.6 的首价静态 image bid/render 响应；币种/尺寸/创意引用一致 |
| AT-02 | 请求无匹配 line item、预算不足或 placement policy 禁止 | 返回协议定义的 no-bid/no-fill；不是成功的伪造 bid，也不是 transport error |
| AT-03 | 对 targeting 运行 domain 精确、page prefix 边界、banner 尺寸和多维 AND 向量 | 仅全部指定维度满足时出价；路径段边界与规范化规则逐向量一致 |
| AT-04 | DSP 延迟、超时、无效 JSON、request ID/imp 不匹配、非法价格/币种或 creative 不白名单 | 该 bidder 候选被拒绝/记 no-bid；其他 bidder 仍可完成；auction 不超过总 deadline |
| AT-05 | 分别发布和不发布配置；并发读取多个实例 manifest | 未发布配置不生效；发布后版本可追踪；陈旧实例按定义不承接受影响流量 |
| AT-06 | 多预算 scope 并发 reservation、租约耗尽、过期、UTC 日期切换和迟到 notice | 扣留要么所有适用 scope 成功要么全部回滚；不越过预算界限；settlement/grace 行为正确 |
| AT-07 | 发送 nurl、lurl、burl；对 loss/billable 重放、交叉竞态及相同 ID 不同 body 测试 | nurl 不计费；loss/billable 互斥；相同内容幂等；冲突隔离；DB 事务失败不返回成功 |
| AT-08 | 发送合法 impression，再重放 SDK token、Supply audience token、过期/篡改/未知 key token 和任意 click | 仅第一个合法 impression 产生一条 payable/spend 链；所有无效/重复/click 均不新增财务分录 |
| AT-09 | 清空 Valkey 后用有效 event token 请求 impression 入账 | SSP 从 token 恢复经认证计费上下文并完成幂等落账；无账务上下文从 Valkey 恢复依赖 |
| AT-10 | 上传伪造 MIME、损坏/超尺寸图片、hash/尺寸不符或未授权 asset_ref | 上传/发布/renderer 按边界拒绝；浏览器不获得存储凭据；任意 HTML/JS 不执行 |
| AT-11 | import snapshot，核验通过前查询、校验失败、重试成功、发布切换 | 未发布/失败 snapshot 永不被解析；失败不影响旧发布版本；成功后只读新不可变版本 |
| AT-12 | subject 解析与 revoke 并发；覆盖多个 key version；注入 Valkey epoch 丢失和 ClickHouse 故障 | 新 admission 立即关闭；在途结果在响应前重检并丢弃；未确认全版本 gate 不报 completed、不发 segments |
| AT-13 | 恢复 DMP 节点/Valkey；重建 deny set；执行快照/主体到期删除及备份恢复 | gate 未重建一致前不 ready；恢复后撤回仍有效；删除符合已批准期限且不误删同分区数据 |
| AT-14 | outbox 遇网络错误、429、5xx、4xx、4xx 重定向/非法目标和 grace 到期 | 网络/429/5xx 按退避重试；终态错误隔离；只投递 allowlist host；过期项保留告警，不静默丢弃 |
| AT-15 | 对 ledger 做部分/全额/重复/并发/超额/币种错误冲正 | 仅允许关联原分录的合法相反方向追加项；非法事务回滚；原始账本记录不可改写 |
| AT-16 | 使用无 consent、撤回、错误 tenant、未授权 Origin、重放 capability/token 请求 Client/Internal API | 请求被拒绝或按定义返回空结果；fail-closed；无跨租户数据、身份或 segment 泄漏 |
| AT-17 | Portal 完成 SDK 加载、filled、no-fill、服务错误、renderer destroy 和过期 render URL | 状态与诊断符合 Client API；token/完整用户对象不显示；iframe/CSP/清理边界有效 |
| AT-18 | 运行目标负载和冷启动/缓存冷却组合 | SSP 入口达 3,000 QPS，P95/P99 ≤100 ms；报告派生 DSP/DMP QPS、错误率、资源、缓存和 queue 指标 |
| AT-19 | 分别停用 MariaDB、Valkey、ClickHouse、DSP，以及触发 outbox 堆积 | 账务事实不丢、不重；非账务事件按批准 loss budget；撤回相关请求 fail-closed；探针和告警正确 |
| AT-20 | 对日志、trace、metrics、错误响应、Portal diagnostics 做敏感值扫描 | 不含原始身份、subject token、AEAD 明文、完整 BidRequest、密钥或对象存储凭据 |
| AT-21 | 浏览器走查 SSP/DSP/DMP 控制台的列表、搜索/筛选、详情、表单校验、发布与撤回流程 | 页面与授权 API 一致；表单错误可恢复；危险操作需确认；窄屏、键盘焦点和可访问名称可用 |

测试用例应记录测试版本、环境拓扑、数据集/负载、开始结束时间、原始指标链接、通过/失败结果、缺陷 ID 和复测结论。测试数据仅使用合成租户、合成 token 和明确登记的静态 creative。

## 7. 发布门禁与责任

### 7.1 责任分工

| 角色 | 责任 |
|---|---|
| 产品负责人 | MVP 边界、计费语义、状态流程、D-02…D-09 业务决策及验收签字 |
| 架构/技术负责人 | D-01/D-10、服务边界、契约/数据一致性、架构偏差审批 |
| SSP/DSP/DMP 研发负责人 | 各自应用实现、数据库所有权、迁移、单元/集成测试和运行指标 |
| 测试负责人 | 测试矩阵、fixtures、环境与数据、缺陷分级、端到端/压力/恢复报告 |
| 安全/隐私负责人 | 安全边界、token/Origin/SSRF/tenant 验证、敏感数据和删除验证；本轮不承担法规适用性评审 |
| 运维/平台负责人 | CI、部署、密钥/证书、监控告警、容量、备份恢复和 runbook |
| 财务/账务负责人 | payable/spend 依据、舍入/冲正/对账语义和隔离处置验收 |

### 7.2 发布阻断条件

以下任一条件未满足，不发布 Sandbox release candidate；生产使用始终需另行批准：

1. 有未关闭的阻塞决策或产品验收未签字。
2. 任一财务幂等、预算、撤回 fail-closed、tenant 隔离或 token 验证测试失败。
3. 账务 outbox/ledger 存在不可解释丢失、重复计费或未隔离冲突。
4. 压测未在确认拓扑下达到 3,000 QPS 与 P95/P99 ≤100 ms，或测试报告缺失。
5. ClickHouse 删除/备份生命周期或 deny gate 恢复演练未达到批准目标。
6. 存在未修复的高风险安全问题、敏感数据泄漏或不可回滚迁移。
7. 缺少值班告警、恢复步骤、回滚记录或测试证据归档。

所有迭代任务以代码、迁移、接口/运维文档、自动化测试和验收证据共同完成为 Done；尚未解决的工作必须保留 owner、影响范围和目标迭代，不得以手工演示替代验收。
