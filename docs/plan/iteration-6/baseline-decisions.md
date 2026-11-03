# Iteration 6 基线决策与架构映射记录

状态：I6-01~I6-07 实现完成（sandbox 单租户拓扑），待生产拓扑与 mTLS 复测
适用分支：`main`（iteration 5 合并提交 `6d29702` 之后）
关联文档：[Iteration 5 基线决策](../iteration-5/baseline-decisions.md) | [首期开发计划](../implementation-plan.md) | [API](../../design/API.md) | [LLD](../../design/LLD.md)

## 1. 目标与范围

Iteration 6 把配信链路补成**唯一账务闭环**：首个有效 impression 在同一 `ssp_db` 事务内写入唯一 billable delivery event、publisher payable 分录与持久 DSP billable outbox；outbox 按登记的 `burl` 投递并重试/隔离；DSP 在 reservation 终态事务中写 advertiser spend ledger；两本账本都只允许追加冲正。

本迭代同时收敛 [Iteration 5 §7.4](../iteration-5/baseline-decisions.md) 记录的 5 项残留限制。

本迭代**不**包含：Publisher 实际付款/开票、跨币种换算、生产 mTLS（D-01）、多实例压测。

## 2. 基线决策

| 决策 ID | 事项 | 基线 |
|---|---|---|
| I6-D01 | 事件幂等键 | 严格按 LLD §2.6：`SHA256("openads.event.v1" \|\| LP(tenant) \|\| LP(auction) \|\| LP(bid) \|\| LP(imp) \|\| LP(event_type) \|\| seq)`，`LP` 为 8 字节大端长度前缀。禁止用接收时间或随机 UUID 作幂等依据。 |
| I6-D02 | 单一记账事务 | 同一事务内插入 `ssp_delivery_event`、`publisher_payable_ledger`、`ssp_billable_outbox`；重复 `source_event_key` 命中唯一键后返回 `duplicate`，不再新增应付，也不重复入队。 |
| I6-D03 | 双 audience 共用账本 | 浏览器 SDK 用 `ssp.event.v1`，Supply 服务端用 `ssp.supply.event.v1`；两个 audience 的 token 绑定同一 event key，写入同一账本行。跨 audience 使用对方 token 一律 400。 |
| I6-D04 | 金额来源 | 单次金额在**签发 token 时**按 `round_half_even(cpm_micros × 1 / 1000)` 算好并封进 `EventClaims.BillableMicros`；tracker 不再用 CPM 重新推导（避免配置/舍入漂移）。 |
| I6-D05 | Outbox 投递语义 | 2xx → `delivered`；`429`/`5xx`/网络错误 → 有界指数退避重试（上限 300s，8 次后 `quarantined`）；其它 4xx → 立即 `quarantined`；`3xx` 视为失败（客户端禁止跟随重定向）。投递 body 与 `burl` profile 一致，重试复用同一 `event_id` 与 `source_event_key`。 |
| I6-D06 | 账本不可变 | 两本账本只允许追加；`entry_type ∈ {billable, reversal, adjustment}`；`reversal` 必须且只能带 `reverses_entry_id`，只能冲正 `billable`，必须与原分录同币种、方向相反，累计冲正绝对值不得超过原金额。冲正按内容幂等（original + amount + reason 相同则返回首次分录）。 |
| I6-D07 | DSP 通知互斥 | `nurl` 只记录 win，不结算；`lurl` 与 `burl` 互为终态，靠 `budget_reservation_settlement` 的唯一键 + 行锁串行化；同 reservation 冲突终态写 `dsp_notice_quarantine(pending)`。 |
| I6-D08 | Grace 与迟到通知 | `budget_lease.settlement_grace_until` 之后到达的有效通知不结算，写 quarantine（`reason_code=grace_expired`）并返回 `410`；SSP outbox 侧保留该行并告警，不静默丢弃。 |
| I6-D09 | 多 scope 全有或全无 | 结算必须覆盖 `Demands` 的每个 scope；任一 scope 缺少 lease 即整体回滚（`ErrInvalidSettlement`），不允许"结算一部分"。 |
| I6-D10 | 运行时 lease 与账本的衔接 | DSP 热路径租约在 Valkey；MariaDB 结算行需要的 lease 外键在结算时按 **reservation** 物化（`sha256("openads.settlement.lease.v1"\|reservation\|scope")→16B`），不复用跨 reservation 共享的运行时 lease id，避免第二次结算命中已过期行。 |

## 3. 数据与迁移

| 迁移 | 内容 |
|---|---|
| [ssp/migrations/000003_ssp_ledger.up.sql](../../../ssp/migrations/000003_ssp_ledger.up.sql) | `publisher_payable_ledger` 增加类型/冲正配对 CHECK、冲正索引、自引用与 delivery event 外键；`ssp_billable_outbox` 状态域扩展为 `pending/sending/delivered/quarantined/rejected` 并新增 `resolved_at`/`resolution_reason`。 |
| [dsp/migrations/000005_dsp_spend_ledger_constraints.up.sql](../../../dsp/migrations/000005_dsp_spend_ledger_constraints.up.sql) | `advertiser_spend_ledger` 增加同等的类型/冲正 CHECK、索引与自引用外键。 |

已确认：两者在 MariaDB 10.6 上 up → down → up 往返成功；空卷初始化脚本与 `make migration-check` 已同步。

## 4. 任务映射与验收证据

| 任务 | 完成 | 交付物 | 验收证据 |
|---|---|---|---|
| I6-01 | ✅ | `token.AudienceSupplyEvent`；`EventClaims` 增加 `PublisherID/PropertyID/BillableMicros`；AEAD 用途/audience/期限/密钥版本校验 | `render/tracker_test.go` 覆盖跨 audience 拒绝、过期、跨租户、篡改 token 与无账本 fail-closed。 |
| I6-02 | ✅ | `ledger.Store.RecordImpression` 唯一 event key + 幂等事务；`POST /sdk/v1/impressions` 与新增 `POST /internal/v1/supply/impressions`（Supply key 鉴权）共用同一账本 | 真库集成测试：首写 `accepted`，重放 `duplicate` 且三表各 1 行；实机 SDK 与 Supply 两条路径分别 accepted/duplicate。 |
| I6-03 | ✅ | 同一事务写 `ssp_delivery_event` + `publisher_payable_ledger` + `ssp_billable_outbox`；`ledger.Sender` 投递 `burl` | `outbox_test.go` 覆盖 payload 字段、2xx/429/5xx/4xx/重试预算/3xx 不跟随；实机 outbox `delivered`。 |
| I6-04 | ✅ | DSP `SettleReservation` 多 scope 全有或全无、grace 校验、按租约物化、`source_event_key` 必填；advertiser spend ledger 归属从 `line_item JOIN campaign` 解析 | 真库测试：迟到通知 → `ErrSettlementExpired` + quarantine 且无分录；缺 scope → 回滚且 `spent_micros=0`；实机 spend=4200。 |
| I6-05 | ✅ | nurl 不结算；lurl/burl 互斥（唯一键 + 行锁）；迟到/冲突写 quarantine；`POST /admin/v1/quarantine/{id}/resolve`（DSP）幂等处置并记录 reason | 既有 `TestLossNoticeAndConflictingBillable`（409）、冲突隔离测试；quarantine 处置实测 200/重放 200/未知 404。 |
| I6-06 | ✅ | `ledger.Store.AppendPayableReversal` + `POST /admin/v1/publisher-payable/{entry_id}:reversal`；DSP `AppendSpendReversal` + `POST /admin/v1/spend-ledger/{id}/reversal` | 真库测试覆盖部分冲正、按内容幂等、超额拒绝、链式冲正拒绝、累计金额校验。 |
| I6-07 | ✅ | `ledger.Sender`：禁止重定向、私网拒绝（复用 netguard）、退避重试、4xx 隔离、`GET /admin/v1/billable-outbox` 与 `:resolve` | `outbox_test.go` 5 个用例；实机 outbox 由 `quarantined` 转为 `delivered`。 |

### 4.1 端到端证据

`ssp/scripts/filled-path.sh`（本地 compose 环境，脚本每次运行使用独立 site domain 与 supply 凭据）：

```
==> SDK ingress returns a filled ad            status=filled
==> render shell and asset bytes               asset SHA-256 与上传一致
==> impression ...                             accepted / duplicate
==> publisher payable is booked exactly once   payable 4200 (USD)
==> billable outbox delivers ...               outbox delivered
==> DSP recorded the advertiser spend          spend 4200
==> click redirects ...                        https://example.com/landing
==> supply ingress returns a seat bid          seatbid 非空，含 ext.openads.event_token
==> Supply reports the impression ...          accepted / duplicate（与 SDK 共用 event key）
==> filled-path integration smoke passed
```

## 5. 验证命令

```bash
# 全模块
for m in identity console ssp dsp dmp; do (cd "$m" && gofmt -l . && go vet ./... && go test ./...); done

# 真库账务
cd ssp && SSP_MARIADB_TEST_DSN="ssp_user:ssp_pass@tcp(127.0.0.1:3306)/ssp_db?parseTime=true" \
  go test ./internal/ssp/ledger/ -v
cd dsp && DSP_MARIADB_TEST_DSN="dsp_user:dsp_pass@tcp(127.0.0.1:3306)/dsp_db?parseTime=true" \
  go test ./internal/dsp/lease/ -run 'TestSettleReservation|TestAppendSpendReversal' -v

# 配信 + 账本全链路（compose 内）
SSP_ADMIN_TOKEN=... DSP_ADMIN_TOKEN=... \
SSP_DSP_BID_URL=http://dsp:8081/openrtb2/bid SSP_DSP_CALLBACK_HOST=dsp \
  ./ssp/scripts/filled-path.sh

make migration-check && docker compose -f deployments/docker-compose.yml config >/dev/null
```

CI（`.github/workflows/go.yml`）已加入上述真库账务测试与 filled-path 冒烟。

## 6. 已知限制与后续项

| # | 项 | 状态 | 说明 |
|---|---|---|---|
| 1 | Publisher 结算导出 | ✅ 只读导出已交付 | `GET /admin/v1/publisher-payable/export?from=&to=` 按 publisher/currency 汇总 billable、reversal 与 net payable；**不是付款指令**，无发票/付款状态（D-05 仍限 sandbox）。 |
| 2 | 运行时 lease 与账本双写 | ⚠️ 已加防护，未收敛 | 结算前检查 `budget_account.leased_micros`，不足时仍记账但写 `dsp_notice_quarantine(reason_code=leased_underflow)`；彻底收敛（单一权威存储）需架构决策。 |
| 3 | nurl 无持久记录 | ✅ 已补齐 | win notice 携带 `ref`，DSP 解析后写 `dsp_delivery_event(event_type='win', amount_micros=NULL)`，幂等且不动预算/账本。 |
| 4 | Outbox 跨实例 | ✅ 已验证 | 新增两 sender 并发测试：12 个事件、5 轮抢占，每个事件恰好投递一次（`FOR UPDATE SKIP LOCKED` + 60s claim 租约）。 |
| 5 | 管理面授权 | ⚠️ 部分 | 账务接口支持独立 `SSP_FINANCE_TOKEN`／`DSP_FINANCE_TOKEN`（未配置时回落到 admin token）；角色化 + mTLS 仍待 D-01。 |
| 6 | 冲正审计 | ✅ 已交付 | 新增 `publisher_payable_audit`／`advertiser_spend_audit`（与分录同事务写入，含 amount/reason/actor/request_id），并提供只读 `GET .../audit`。 |
| 7 | 多币种 | ❌ 未做 | 仍为单币种精确匹配；汇率与跨币种对账属产品/财务决策。 |
| 8 | 付款/开票 | ❌ 未做 | 账本只到应付金额；实际 payout/remittance 与付款状态是独立用例。 |

## 7. 后续收敛记录（2026-11-03 第二轮）

在上述 §6 基础上继续收敛，均带真库或实机证据：

| 项 | 变更 | 证据 |
|---|---|---|
| 冲正审计 | 迁移 [ssp/000004](../../../ssp/migrations/000004_ssp_ledger_audit.up.sql)、[dsp/000006](../../../dsp/migrations/000006_dsp_spend_audit.up.sql)；冲正与审计同事务写入；`GET /admin/v1/publisher-payable/audit`、`GET /admin/v1/spend-ledger/audit` | 真库测试断言 action/amount/actor/request_id；实机 `X-Actor: finance.sandbox` 写入并回读 |
| 结算导出 | `Store.ExportPayable` + `GET /admin/v1/publisher-payable/export`（≤400 天窗口，倒置区间 400） | 真库测试断言 billable 2500 / reversal 1000 / net 1500；实机按月窗口返回各 publisher 汇总 |
| win 归因 | `ReservationClaims.LineItemID`；nurl/lurl 均带 `ref`；`RecordWinNotice` 写非计费事件 | 真库测试断言 `win` + NULL 金额 + 正确归属，且无 spend/settlement |
| lease 漂移 | 结算前读 `leased_micros`，不足即写 `leased_underflow` quarantine | 真库测试：注入漂移后仍记 spent=2500 且 quarantine=1 |
| 多实例 outbox | 并发 sender 测试 | 12 事件 × 2 sender × 5 轮，投递次数严格为 1 |
| 财务 token | `SSP_FINANCE_TOKEN`／`DSP_FINANCE_TOKEN` 可独立授权账务接口 | 配置项接入，未配置时回落 admin token（保持沙箱可用） |
