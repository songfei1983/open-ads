# Iteration 2 基线决策与架构映射记录

状态：开发中
适用分支：`feat/iteration-2`
关联文档：[Iteration 1 基线决策](../iteration-1/baseline-decisions.md) | [首期开发计划](../implementation-plan.md) | [API](../../design/API.md) | [OpenRTB fixtures](../../fixtures/openrtb2/)

## 1. 目标与范围

Iteration 2 聚焦基础 RTB 协议与请求流：先固定 SSP/DSP 的 OpenRTB 2.6 wire 边界，再完成单 bidder 调用、deadline/no-bid 语义和可重复的 fixture 驱动测试。本迭代不实现多 DSP 拍卖、预算预留、真实 campaign 决策、素材白名单或账务结算。

## 2. 基线决策

| 决策 ID | 事项 | 基线 |
|---|---|---|
| I2-D01 | OpenRTB profile | 首期支持单展位 Image Banner、单币种 USD、`at=1` first-price CPM；请求和响应必须保持相同 `id`，bid 必须引用已存在的 `impid`。 |
| I2-D02 | SSP ingress | `/openrtb2/auction` 限制请求体 256 KiB，至少要求 `id` 和非空 `imp`；结构非法返回 `400 invalid_request`，无候选返回 `200` + `nbr=2`。 |
| I2-D03 | DSP bidder | `/openrtb2/bid` 对请求做基础结构校验；当前决策端口默认返回标准 no-bid，不读取 MariaDB，不记录完整 BidRequest。 |
| I2-D04 | deadline 与取消 | SSP 使用剩余 auction context 调用 DSP，并施加 `DSP_TIMEOUT_MS`；超时、取消、连接错误和非 2xx 均映射为 no-bid。 |
| I2-D05 | request ID 关联 | SSP 下游请求通过 `X-Request-ID` 传递请求关联 ID；OpenRTB body `id` 是协议关联 ID，响应 ID 不匹配时不得成为有效候选。 |

## 3. 当前完成情况

| 任务 | 当前状态 | 证据 |
|---|---|---|
| I2-01 | 进行中 | SSP/DSP 已有独立 HTTP DTO 和 banner/no-bid response；仍需补齐 fixture round-trip 和字段边界测试。 |
| I2-02 | 基础完成 | SSP ingress 已有 body limit、`id`/`imp` 校验、request ID 和 no-bid 映射。 |
| I2-03 | 基础完成 | DSP bidder 已校验 `id`/`imp` 并返回标准 no-bid；可替换决策端口尚未抽取。 |
| I2-04 | 基础完成 | SSP 单 bidder client 已支持 context、独立 timeout、非 2xx、非法 JSON 和响应 ID mismatch 降级。 |
| I2-05 | 进行中 | 已有 SSP mock DMP/DSP 联通测试和 OpenRTB request ID 往返测试；仍需本地双进程 smoke 命令和 CI 双服务启动验证。 |

## 4. 下一步验收

1. 将 OpenRTB request/response DTO 从 handler 内联结构整理为服务内 wire package，保持 SSP/DSP 类型独立。
2. 用 `fixtures/openrtb2/` 增加 banner request/response round-trip、非法 imp、currency 和 ID mismatch 测试。
3. 抽取 DSP `BidDecisioner` 接口，保留默认 no-bid 实现，供 Iteration 3 接入热路径决策。
4. 增加 DSP 延迟注入测试，证明超过 deadline 时 SSP 返回 no-bid 且不泄漏 goroutine。
5. 增加本地 smoke 命令，启动 SSP/DSP 后执行一次 bid/no-bid 往返，并纳入 CI。
