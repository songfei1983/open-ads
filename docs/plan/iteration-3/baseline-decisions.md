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
- 已有进程内 manifest、fixture decisioner 和轻量 lease allocator 的占位实现；下一步补齐不可变发布快照与 readiness 版本、真实 targeting 和基于预算金额的 Valkey lease/reservation，并接入出价路径。

## 4. 任务映射

| 任务 | 完成 | 下一步交付物 | 验收重点 |
|---|---|---|---|
| I3-01 | 🟡 部分实现 | Campaign/Line Item/Creative 管理 DTO、版本校验和发布状态 | 当前只有基础领域结构和简单版本字符串校验；需实现控制 DTO、配置版本冲突校验及发布状态，未发布配置不得影响 bidder。 |
| I3-02 | 🟡 部分实现 | 静态素材元数据和白名单校验 | 当前已有素材元数据与白名单结构；需补齐 MIME、尺寸、hash、landing host 校验，并确保不执行任意 HTML/JS。 |
| I3-03 | 🟡 部分实现 | 不可变发布快照、刷新和 readiness 实际加载版本 | 当前 manifest 仅提供进程内按版本存取和空快照刷新；需保证版本不可变，cache miss 不回源 MariaDB，并能识别陈旧实例。 |
| I3-04 | 🟡 部分实现 | Fixture decisioner 和基于配置快照的 banner targeting | 当前 fixture 仅以 request ID 模拟 host，并检查是否存在 Campaign；需实现 domain/page/banner 精确匹配，非法候选不得出价。 |
| I3-05 | 🟡 部分实现 | 基于预算额度的 Reservation lease allocator 并接入 bidder | 当前 allocator 仅按 scope 做内存全有或全无占用及幂等释放；需完成多 scope 原子额度检查，额度不足不出价，reservation 释放幂等。 |
| I3-06 | ✅ 部分实现 | 完整的 bidder 决策、超时和快照指标 | 已有 Decisioner 调用边界、默认 no-bid、请求超时中间件和基础 HTTP 指标；仍需区分 no-bid、候选、决策错误和超时，并补齐不含敏感标签的快照/决策指标。 |
