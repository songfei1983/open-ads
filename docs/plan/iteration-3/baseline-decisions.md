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
- 下一步实现配置快照模型和可替换的 fixture decisioner，再接入 Valkey lease/reservation。

## 4. 任务映射

| 任务 | 下一步交付物 | 验收重点 |
|---|---|---|
| I3-01 | Campaign/LineItem/Creative 管理 DTO、版本校验和发布状态 | 未发布配置不影响 bidder；版本冲突返回稳定错误。 |
| I3-02 | 静态素材元数据和白名单模型 | MIME、尺寸、hash、landing host 校验；不执行任意 HTML/JS。 |
| I3-03 | 快照 manifest、刷新和 readiness 版本 | cache miss 不回源 MariaDB；陈旧实例可识别。 |
| I3-04 | Fixture decisioner 和 banner targeting | domain/page/banner 精确匹配；非法候选不得出价。 |
| I3-05 | Reservation lease allocator | 多 scope 全有或全无；额度不足不出价；释放幂等。 |
| I3-06 | bidder 决策、超时和快照指标 | no-bid、候选、错误和超时可区分且不含敏感标签。 |
