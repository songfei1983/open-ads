# Iteration 3 基线决策与架构映射记录

状态：I3-01~I3-06 完成；I3-07 未开始
适用分支：已合并至 `main`（PR #40，2026-10-26）
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
- 已有进程内不可变 manifest、readiness 快照版本、domain/page/banner targeting 候选检查、固定类别决策指标和 reservation 原语；I3-03 已接通当前已发布投影、Valkey 刷新与实例版本报告，I3-04 已完成确定性候选选择。
- I3-01 新增控制 API 与 MariaDB 存储；publish 在同一事务更新当前已发布投影并写待处理 outbox，不直接切换 bidder 快照。已按当前开发阶段约定只保存每个资源的当前草稿和当前已发布投影，不归档多份发布历史。
- 最近验证：在 `dsp/` 下 `go build ./...`、`go mod verify` 和 `git diff --check` 通过；I3-02 的 PNG/JPEG/WebP 静态图片上传校验已实现。本次未运行测试，也未做 MariaDB/Valkey 集成验收。

## 4. 任务映射

| 任务 | 完成 | 下一步交付物 | 验收重点 |
|---|---|---|---|
| I3-01 | ✅ 完成 | — | 已实现 Advertiser/Campaign/Line Item/Creative 列表、创建、更新 API；更新使用版本行锁并在冲突时返回稳定 `409 version_conflict`。发布会验证资源状态、引用、预算及 MVP targeting，并事务写入 config outbox；草稿更新不写 outbox、不会改动 bidder 当前 manifest。按当前开发阶段决策仅保留当前资源版本；发布扇出与快照刷新由 I3-03 完成。 |
| I3-02 | ✅ 完成 | — | 已新增 PNG/JPEG/静态 WebP 上传、10 MB 与 4096×4096 限制、格式/解码校验、拒绝 APNG/动画 WebP、服务端 SHA-256、tenant+hash 不可覆盖文件存储、元数据 registry，以及 Creative 创建/发布时的元数据和文件 hash 复核。构建、依赖完整性和 diff 空白检查通过；尚无图片安全测试及 MariaDB 集成验收。 |
| I3-03 | ✅ 完成 | — | MariaDB 每资源仅保存一行当前已发布投影；发布事务同时更新投影、单调 tenant manifest 序号和 outbox。publisher 通过带版本防回退的 Lua 原子写入完整 Valkey manifest，持久成功后确认 outbox；失败记录脱敏原因并按指数退避重试，周期性重建可修复 Valkey 丢失。实例轮询并验证 schema/hash/version 后原子激活本地快照，按 TTL 上报 incarnation、已加载/观测版本和 readiness；超过 10 秒不同步后 readiness 失败，状态索引自动清理过期实例。 |
| I3-04 | ✅ 完成 | — | 已将 OpenRTB site/domain/page/banner/currency 映射为 transport-neutral 请求；manifest 保留全部 domain/page-prefix/banner targeting、资源生效时间、币种和 bid CPM。决策器按时间窗、币种、targeting、creative 白名单过滤，并按 CPM 降序、ID 稳定打破平局选出候选；响应继续返回标准 no-bid，只有 I3-05 reservation 成功后才可出有效 bid。 |
| I3-05 | ✅ 完成 | — | 已实现 Daily/lifetime BudgetAccount、MariaDB 租约分配与回收（SQLBudgetStore）、Valkey 与本地两阶段原子扣留预留；AEAD token (AES-256-GCM) 签发与解封校验；接入 OpenRTB 出价热路径（有租额出价、无租额安全 no-bid）；打通 win/loss/billable 通知处理、幂等确认、冲突隔离（dsp_notice_quarantine）及 spend ledger 记录。 |
| I3-06 | ✅ 完成 | — | 已有 Decisioner 调用边界、默认 no-bid、请求超时中间件和基础 HTTP 指标；固定类别的候选、no-bid、决策错误、超时计数及决策耗时。快照加载/陈旧度指标已补齐并在线验证：`open_ads_dsp_snapshot_loads_total`、`snapshot_load_errors_total`、`snapshot_staleness_seconds`、`snapshot_load_duration_seconds` 直方图（记录于同步循环 `dsp/cmd/dsp/main.go`，经 `/metrics` 暴露）。 |
| I3-07 | ⚪ 未开始 | DSP 原型管理页面与 Go API 联调 | UI 原型已从 DSP 拆出为独立 `console/` 服务（:8083），但管理页仍为纯演示数据（`console/web/console.js` 本地模拟，不调用服务端）。剩：Campaign、Line Item、Creative 管理接通 DSP `/admin/v1`，展示真实发布结果、`409 version_conflict` 与发布确认机制。沙箱投放链路本身已通过 admin API + SSP 模拟器端到端验证（campaign→publish→快照→bid）。 |
