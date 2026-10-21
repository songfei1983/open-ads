# Open Ads — Iteration 0 基线决策与架构映射记录

状态：已确认（Iteration 0 交付物）
适用分支：`feat/iteration-0`
关联文档：[系统概要设计](../../design/README.md) | [HLD](../../design/HLD.md) | [LLD](../../design/LLD.md) | [API](../../design/API.md) | [首期开发计划](../implementation-plan.md)

---

## 1. 跨职能决策项封板记录（D-01 ~ D-10）

根据 [implementation-plan.md §2](../implementation-plan.md)，以下 10 项核心决策在 Iteration 0 全部封板并确定落地基线：

| 决策 ID | 事项名称 | 封板结论 / 实施基线 | 状态 |
|---|---|---|---|
| **D-01** | 部署地域与网络拓扑 | 首期采用独立容器网络（Docker Compose / Kubernetes Namespace），内部微服务间通信使用服务名与 mTLS 1.3；外部仅暴露 SSP `/openrtb2/auction`、`/sdk/v1/*` 及管理网关，MariaDB/ClickHouse/Valkey 不直接对外暴露公网。 | **已关闭 (CLOSED)** |
| **D-02** | 超时预算分配与大小限制 | OpenRTB 总竞价截止时间 `tmax = 80ms`。SSP 内部耗时预算切分：DMP 受众解析 `15ms`（可并行或超时自动降级为无受众竞价）；DSP 并发竞价 `50ms`；SSP 内部网络与拍卖编排 `15ms`。HTTP 报文上限：API 请求体最大 64KB，创意素材最大 2MB。 | **已关闭 (CLOSED)** |
| **D-03** | OpenRTB 2.6 首期 Profile | 仅支持单展位 Image Banner、单币种 USD、first-price CPM（`at=1`）。宏替换支持 `${AUCTION_PRICE}` 与 `${AUCTION_LOSS}`。固化标准 JSON 见 `fixtures/openrtb2/`。 | **已关闭 (CLOSED)** |
| **D-04** | DSP 超花容忍度与预算租约 | 严格 0% 超花容忍。通过两阶段租约机制：实例在出价前从本地 Lease 扣留单次最大风险（Reservation），仅在收到 `burl` 计费回调或 `lurl` 失败通知时在 MariaDB 事务中最终结算并释放差额。租约空闲心跳 5 秒，宽限期 300 秒。 | **已关闭 (CLOSED)** |
| **D-05** | Sandbox 账本与收益结算 | 沙箱环境下 Publisher Payable 暂按有效 DSP 计费额度 100% 记账入库（无 SSP 抽成与税费）。所有账本金额一律使用微单位 `BIGINT SIGNED` 存储（`1 USD = 1,000,000 micros`），杜绝浮点累加。 | **已关闭 (CLOSED)** |
| **D-06** | 数据生命周期与归档策略 | 身份映射（Subject Mapping）和受众快照保留 3 个月；DMP 分析日志保留 12 个月；用户撤回（Tombstone）持久化保留并执行 30 天物理覆写清理；数据库备份保留 90 天滚动周期。 | **已关闭 (CLOSED)** |
| **D-07** | 撤回生效 SLA 与陈旧度限制 | 隐私撤回门禁（Deny Gate）在线生效 SLA：P99 < 50ms。一旦触发撤回，DMP 与 SSP 共享的 Valkey Deny Gate 立即置位，阻断所有新竞价；ClickHouse 异步持久化墓碑；SSP/DMP 本地缓存最大陈旧时间为 5 秒。 | **已关闭 (CLOSED)** |
| **D-08** | 分析事件丢失容忍与重试 | 交易与计费事实由 MariaDB 事务 Outbox 保证 At-Least-Once 零丢失；普通竞价日志与受众分析事件走 Valkey Stream 缓冲，缓冲上限 100,000 条，保留期 24 小时，超出则告警并按策略淘汰。 | **已关闭 (CLOSED)** |
| **D-09** | 供给侧合规与测试边界 | 首期仅对接固定配置的 Sandbox 供给方，暂不执行全网 `ads.txt` 与 `sellers.json` 自动抓取与校验，生产流量接入前再行启动相关合规系统。 | **已关闭 (CLOSED)** |
| **D-10** | ClickHouse 受众查询 PoC 结论 | 采用按 `(tenant_id, namespace, subject_token_hash)` 分区建立主键与 Bloom Filter，千万级数据量下主键点查延迟 < 8ms，满足受众解析需求。评估结论：**GO**。 | **已关闭 (CLOSED)** |

---

## 2. 威胁边界与安全测试规范（I-04）

1. **同源策略与渲染沙箱（Same-Origin & Sandbox）**：
   - 广告创意展示一律采用 SSP 托管的 HTML 隔离壳（`iframe sandbox="allow-scripts"`，禁止 `allow-same-origin`）。
   - 广告渲染 URL 强制同源校验，禁止第三方跨域篡改（已在 `docs/prototype/app.js` 固化验证）。
2. **SSRF 与重定向防护**：
   - DSP 回调 URL（`nurl`/`burl`/`lurl`）以及创意点击跳转 URL 必须由服务端严格校验协议（强制 HTTPS）及域名白名单，禁止解析到内网私有地址（10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 127.0.0.1）。
3. **AEAD 凭据防伪**：
   - 客户端携带的 `render_token`、`event_token` 和 `reservation_ref` 采用 ChaCha20-Poly1305 或 AES-GCM 加密签名，防重放、防篡改且限定 5 分钟短有效期。
4. **日志隐私脱敏（Zero-Log PII）**：
   - 禁止在任何标准日志或调试输出中打印用户原始 IP、Cookie 明文、原始设备号、Consent 明文字符串及完整 BidRequest 报文。

---

## 3. 架构验收与后续迭代 Epic 映射矩阵（I-05）

将架构文档中的验收标准直接分解映射至后续 7 个 Iteration 的开发任务与验收门禁：

| 迭代 | Epic 名称 | 对应任务 | 核心验收门槛 |
|---|---|---|---|
| **Iteration 1** | 工程底座与可运行骨架 | I1-01 ~ I1-06 | 三大服务独立 Go Module 启动；MariaDB/ClickHouse/Valkey 独立账号与迁移脚本跑通；CI 代码检查与单元测试通过 |
| **Iteration 2** | 基础 RTB 协议与请求流 | I2-01 ~ I2-05 | OpenRTB 2.6 Wire DTO 映射；SSP 单 Bidder 调用打通；超时 no-bid 与协议往返测试通过 |
| **Iteration 3** | DSP 控制面与决策热路径 | I3-01 ~ I3-06 | Campaign/LineItem 配置发布；两阶段预留租约（Reservation Lease）在内存与 Valkey 跑通；出价不回源 MariaDB |
| **Iteration 4** | SSP 拍卖与出价编排 | I4-01 ~ I4-06 | 并发调用多 DSP；First-Price 拍卖获胜比选与 Tie-breaker 判定；DMP 并行受众解析与降级保护 |
| **Iteration 5** | 渲染、归因与结算闭环 | I5-01 ~ I5-06 | 静态素材只读渲染；曝光上报触发 MariaDB Outbox；幂等 Publisher 收益与 DSP 支出落账 |
| **Iteration 6** | DMP 受众与撤回门禁 | I6-01 ~ I6-06 | ClickHouse 会员快照发布；在线受众解析；隐私撤回秒级 Deny Gate 门禁与墓碑处理 |
| **Iteration 7** | 全链路联调与沙箱验收 | I7-01 ~ I7-06 | Test Portal 完整串联真实 Go API；端到端 P99 < 100ms 压力测试；故障自愈与财务对账核销验收 |

---

## 4. 结论与准出

Iteration 0 所有 6 项任务（I0-01 至 I0-06）已满足准出条件，契约 Fixtures 已固化，决策与架构映射已就绪，已满足向 Iteration 1（工程底座与可运行骨架搭建）推进的标准。
