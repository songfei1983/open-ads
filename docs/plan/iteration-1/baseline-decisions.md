# Iteration 1 基线决策与架构映射记录

状态：已实现基础基线，部分能力进入 Iteration 2 延伸
适用分支：`feat/iteration-1`
关联文档：[Iteration 0 基线决策](../iteration-0/baseline-decisions.md) | [首期开发计划](../implementation-plan.md) | [HLD](../../design/HLD.md) | [LLD](../../design/LLD.md) | [API](../../design/API.md)

本文记录 Iteration 1“工程底座与可运行骨架”的落地决策、验证证据和未完成项。本文不把尚未具备真实业务语义的 stub 标记为生产能力；DMP 在线受众、DSP 决策和账务闭环仍按后续迭代验收。

## 1. 基线决策

| 决策 ID | 事项 | Iteration 1 基线 | 状态 |
|---|---|---|---|
| I1-D01 | 服务边界 | SSP、DSP、DMP 保持独立 Go Module、独立配置和启动入口；不共享领域 package。跨服务仅通过 HTTP/JSON 契约通信。 | 已关闭 |
| I1-D02 | HTTP 栈与生命周期 | 使用标准库 `net/http`；统一 request ID、deadline、结构化日志、panic recovery、health/readiness 和 graceful shutdown。服务启动前 not ready，成功进入监听后 ready，关闭时撤回 ready。 | 已关闭 |
| I1-D03 | 请求安全边界 | 自有 JSON 请求体限制为 64 KiB；OpenRTB 请求体限制为 256 KiB；SSP 对 SDK JSON 使用严格字段解析；下游请求继承上游 context，并配置独立 DMP/DSP timeout。 | 已关闭 |
| I1-D04 | 依赖隔离 | MariaDB 分为 `ssp_db` 与 `dsp_db`；Valkey 使用独立 ACL 用户和 keyspace；DMP 使用独立 ClickHouse 用户/数据库。Compose 和 CI 均验证基础设施可启动。 | 已关闭 |
| I1-D05 | 数据与金额基础 | ID 使用小写 32-hex UUID wire 表示；金额领域基础使用有符号微单位整数；不使用 binary float 进行金额累计。 | 已关闭 |
| I1-D06 | 运行时配置 | 每个服务暴露 `CONFIG_VERSION`，readiness 响应返回 `config_version`，用于实例版本追踪；未配置时使用开发默认值 `dev`。 | 已关闭 |
| I1-D07 | 可观测性 | `/metrics` 暴露低基数 HTTP 请求数、错误数、延迟 sum/count/bucket；日志保留 request ID、路径、状态和耗时，不记录完整请求体或敏感 token。Prometheus、告警规则和 Grafana 初版配置位于 `deployments/observability/`。 | 已关闭 |
| I1-D08 | 联通范围 | Iteration 1 只建立可测试的 SSP→DMP audience resolve→DSP bidder 骨架。DMP 当前返回空受众快照，DSP 当前可返回标准 no-bid；真实受众查询、竞价策略和素材白名单不提前承诺。 | 已关闭（骨架） |

## 2. 架构映射与验收证据

| 任务 | 架构/代码映射 | 验证结果 | 状态 |
|---|---|---|---|
| I1-01 | `ssp/`、`dsp/`、`dmp/` 各自 `go.mod`、`cmd/*`、`internal/*/config` | 三模块分别 `go build ./...`、独立配置加载和优雅关闭通过 | 已完成 |
| I1-02 | 各服务 HTTP server、middleware、`/healthz`、`/readyz`、`/metrics` | 单测和 race 测试通过；ready 生命周期和 request ID 已验证 | 已完成 |
| I1-03 | [`deployments/docker-compose.yml`](../../deployments/docker-compose.yml)、MariaDB init、Valkey ACL、ClickHouse migration | MariaDB/Valkey/ClickHouse 健康检查通过；数据库、用户和 keyspace 隔离已验证 | 已完成 |
| I1-04 | [`Makefile`](../../Makefile)、[`.github/workflows/go.yml`](../../.github/workflows/go.yml)、各服务 migrations | format、vet、unit、build、migration wiring 已接入；迁移正向执行和 CI 校验通过 | 基线完成 |
| I1-05 | 各服务 domain UUID、money 组件；配置 `CONFIG_VERSION` | UUID、微单位金额和配置版本已有自动化覆盖；统一 clock abstraction 尚未独立抽取 | 部分完成 |
| I1-06 | [`deployments/observability/prometheus.yml`](../../deployments/observability/prometheus.yml)、[`alerts.yml`](../../deployments/observability/alerts.yml)、[`grafana-dashboard.json`](../../deployments/observability/grafana-dashboard.json) | 请求量、错误率、P95 延迟及服务不可用告警具备初版配置；队列积压指标尚未接入 | 部分完成 |

## 3. Iteration 1 退出门槛

| 门槛 | 结论 |
|---|---|
| 三服务可分别构建、配置和启动 | 通过。 |
| 数据库隔离、ACL 和迁移 wiring 可验证 | 通过。已有 Docker 测试卷的历史初始化污染不作为新环境基线；空卷需按修正后的 init 顺序验证。 |
| CI 可执行 format、vet、unit 和 migration validation | 通过。 |
| 健康探针、readiness 和 request ID 工作 | 通过。readiness 同时返回配置版本。 |
| 监控能关联 request ID、配置版本、异常和延迟 | 基础能力通过；配置版本在 readiness 中可见，metrics 使用低基数标签；队列积压尚未有真实队列指标。 |

## 4. 已提前建立的 Iteration 2 接口基线

Iteration 1 分支已提前落下 I2 的最小请求流骨架，便于后续继续开发：

1. SSP `/sdk/v1/ad-requests` 校验 placement、slot 和请求体大小。
2. consent 存在且配置了 `DMP_URL` 时，SSP 调用 `/internal/v1/audiences:resolve`。
3. SSP 生成单展位 OpenRTB banner 请求，调用 `${DSP_URL}/openrtb2/bid`。
4. DSP response 的 request ID 不匹配、超时、非 2xx 或非法 JSON 均按 `no_fill` 处理。
5. SSP `/openrtb2/auction` 支持基础 OpenRTB 结构校验和单 bidder 转发；无 bidder 或依赖失败返回标准 no-bid。

该骨架已经有 mock DMP/DSP 联通测试，但尚未等同于真实广告策略、素材白名单、token 签发或账务闭环验收。

## 5. 未完成项与后续映射

| 未完成项 | 后续任务 | 处理原则 |
|---|---|---|
| 统一 clock abstraction、可注入时间和完整 fixture harness | I1-05 / I2 | 先抽取服务内 clock 接口，再补时间边界测试；不引入跨服务共享领域包。 |
| 队列积压、依赖状态和 bidder 结果指标 | I1-06 / I2-03 | 增加低基数业务指标，禁止把 request ID、subject token、URL 或完整 bid body 作为 label。 |
| DSP 可替换决策端口和有效 bid fixture round-trip | I2-01、I2-03、I2-05 | 先固定 OpenRTB DTO 和 response ID/imp/currency 校验，再接入真正决策端口。 |
| DMP ClickHouse 在线解析、deny gate 和撤回 | Iteration 4 | 当前空 segments 只表示安全 stub，不得对外宣称真实受众命中。 |
| 渲染 token、素材白名单、impression 和账务 outbox | Iteration 5/6 | 未完成 AEAD、一次性约束和账务事务前，filled 响应仅用于 sandbox 联通测试。 |

## 6. 结论

Iteration 1 的工程底座退出门槛已基本满足：三服务、基础设施隔离、迁移 wiring、CI、探针和基础 metrics 均已落地并验证。I1-05 的统一 clock abstraction 与 I1-06 的真实队列积压观测仍是明确遗留项，不阻塞进入 Iteration 2，但在宣称 Iteration 1 完整关闭前必须补齐或记录为正式后续项。
