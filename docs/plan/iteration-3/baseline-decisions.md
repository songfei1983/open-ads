# Iteration 3 基线决策与架构映射记录

状态：开发中
适用分支：`feat/iteration-3`
关联文档：[Iteration 2 基线决策](../iteration-2/baseline-decisions.md) | [首期开发计划](../implementation-plan.md) | [LLD](../../design/LLD.md)

## 1. 目标与范围

Iteration 3 聚焦 DSP 控制面与决策热路径：建立 Campaign、Line Item、Creative 的配置发布边界，将已发布配置转换为内存快照，并在不访问 MariaDB 的 bidder 热路径上完成基础决策。本阶段不实现真实结算和多 DSP 拍卖。

## 2. 基线决策

| 决策 ID | 事项 | 基线 |
|---|---|---|
| I3-D01 | 决策端口 | HTTP adapter 只负责 OpenRTB 解析和协议响应；通过服务内 `Decisioner` 接口调用决策逻辑，避免 transport 直接依赖数据库或配置表。 |
| I3-D02 | 默认安全行为 | 没有已发布快照、决策器错误或预算不可用时返回 no-bid，不回源 MariaDB，不猜测 campaign 或价格。 |
| I3-D03 | 快照边界 | 后续发布流程生成不可变 manifest/config version；bidder 只读取本地快照或 Valkey 热数据，并在 readiness 中报告加载版本。 |
| I3-D04 | 预算顺序 | 预算租约和 reservation 在出价前完成原子风险检查；未确认租约不得返回有效 bid。 |

## 3. 当前起点

- DSP bidder 已有 `Decisioner` 接口和默认 `NoBidDecisioner`。
- OpenRTB wire 校验仍位于 DSP HTTP adapter，决策包不依赖 OpenRTB DTO。
- 已有进程内不可变 manifest、readiness 快照版本、domain/page/banner targeting 候选检查、固定类别决策指标和本地金额 reservation 原语；下一步补齐发布配置构建、Valkey 多实例原子 reservation 与结算，并接入完整出价路径。

## 4. 任务映射

| 任务 | 完成 | 下一步交付物 | 验收重点 |
|---|---|---|---|
| I3-01 | 🟡 部分实现 | Campaign/Line Item/Creative 管理 DTO、版本校验和发布状态 | 当前只有基础领域结构和简单版本字符串校验；需实现控制 DTO、配置版本冲突校验及发布状态，未发布配置不得影响 bidder。 |
| I3-02 | 🟡 部分实现 | 发布阶段完整素材校验与内容 hash 校验 | 当前候选检查仅接受常见静态 image MIME，要求正数尺寸、非空 hash，并精确匹配 landing host/MIME 白名单；仍需在发布阶段统一校验、校验 hash 格式/内容并明确尺寸上限。 |
| I3-03 | 🟡 部分实现 | 已发布配置构建/刷新流程和陈旧实例识别 | 当前进程内 manifest 已校验 campaign/line item/creative 引用、拒绝覆盖版本、隔离读写副本并原子切换当前版本；readiness 报告实际快照版本且无快照时 not-ready。仍需从持久化已发布配置构建快照、刷新/过期策略及陈旧实例识别。 |
| I3-04 | 🟡 部分实现 | 发布配置的 targeting 编译、候选出价和协议响应 | 当前已从 OpenRTB adapter 映射 site/domain/page/banner 到 transport-neutral request，并基于 active campaign/line item/creative 做精确匹配；候选仍 no-bid，需待预算 reservation 接入后才允许有效出价。 |
| I3-05 | 🟡 部分实现 | Valkey 原子脚本、后台 lease 分配/续租、notice 结算和 bidder 接入 | 新增本地金额 allocator：多 scope 全有或全无、reservation ID 幂等、终态冲突拒绝、过期 lease 不可消费；仍是单进程实现，尚未接 Valkey/MariaDB，也未接入有效 bid 路径。 |
| I3-06 | 🟡 部分实现 | 快照加载/陈旧度指标及端到端超时覆盖 | 已有 Decisioner 调用边界、默认 no-bid、请求超时中间件和基础 HTTP 指标；新增固定类别的候选、no-bid、决策错误、超时计数及决策耗时，无请求标识标签。仍需补齐快照加载/陈旧度指标。 |
