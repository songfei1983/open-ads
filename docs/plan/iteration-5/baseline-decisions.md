# Iteration 5 基线决策与架构映射记录

状态：I5-01~I5-08 完成（sandbox 单租户、单 Region 拓扑）；2026-11-03 追加 Iteration 6 前置缺陷修复，见 §7
适用分支：已合并至 `main`（合并提交 `6d29702`）
关联文档：[Iteration 4 基线决策](../iteration-4/baseline-decisions.md) | [首期开发计划](../implementation-plan.md) | [API](../../design/API.md) | [LLD](../../design/LLD.md)

## 1. 目标与范围

Iteration 5 把 SSP 从骨架补成可验收的控制面 + 端到端配信闭环：Publisher/Property/Placement/AuctionPolicy/Supply/DSP endpoint 经版本化发布进入 Valkey manifest 与进程快照；Supply 与 Portal/SDK 两条入口都能完成 filled/no-fill/error；DSP 并行 fan-out 后按 first-price CPM 选出唯一胜者；胜出素材只以 SSP 托管的静态图片渲染，不执行 DSP HTML/JavaScript；DMP 查询使用 HMAC 伪名 token 并在不可用时 fail-closed。

本迭代**不**开放真实账务。Impression 只做 token 校验与进程内幂等去重，唯一账本、publisher payable、DSP billable outbox 与冲正仍属 Iteration 6。

## 2. 基线决策

| 决策 ID | 事项 | Iteration 5 基线 |
|---|---|---|
| I5-D01 | 租户与供给身份 | 每个 SSP 进程只服务一个 `SSP_ADMIN_TENANT_ID`；tenant 从配置进入，Supply tenant 从凭据哈希解析，绝不从请求体推断。Supply 凭据以 SHA-256 摘要存库并编译进快照，不保存明文。 |
| I5-D02 | 配置发布模型 | 管理写入只更新 draft、`version` 与 `published_version` 分离；publish 在同一事务校验引用并写 `ssp_published_config` + `ssp_config_outbox`。outbox worker 用 `FOR UPDATE SKIP LOCKED`，只有 Valkey manifest 持久成功后才 `published_at`。 |
| I5-D03 | 快照边界 | 热路径只读进程内不可变 manifest（`ssp:manifest:{tenant}` 版本封套 + schema/hash 校验）。无快照时 SDK/Supply 返回 503 `config_unavailable`，管理 API 继续可用。 |
| I5-D04 | 拍卖语义 | 首期只支持 `at=1`、`dsp_priority_then_ssp_hash`、strict floor、exact-match 币种；publish 时拒绝其他值。每个 DSP 独立 deadline，超时/错误/非法响应不阻塞其他 bidder；非法 bid 不参与胜者选择。 |
| I5-D05 | 公开动作路径 | 管理 API 同时接受 `/{id}/publish` 与 `/{id}:publish`；outbox 接受 `/retry` 与 `:retry`；内部同步同时接受 `/creative-registry/upsert` 与 `creative-registry:upsert`。兼容写法仅影响路由，不改变语义。 |
| I5-D06 | 素材边界 | SSP 不信任 `adm`。胜者必须命中已同步白名单的 `creative_id` 且 `ext.openads` 的 `asset_ref`/`asset_sha256`/尺寸/`landing_url` 与白名单完全一致；`nurl`/`lurl`/`burl` host 必须命中 endpoint allowlist，否则整条 bid 丢弃。 |
| I5-D07 | 渲染模型 | `render_url`、`click_url`、`event_token` 均为 AES-256-GCM AEAD token（用途/密钥版本/audience 分离）。`/render/v1/{token}` 只返回 SSP HTML 壳；`/render/v1/assets/{token}` 从共享只读卷按 token 内 `asset_ref` 读取并流式重算 SHA-256。文件缺失/哈希不符返回 503，不出占位图。 |
| I5-D08 | DMP 适配 | SSP 只发送 `HMAC-SHA256(namespace, publisher pseudonym)` 64-hex token；DMP 的 tenant 由 `SSP_DMP_TENANT_ID` 配置（sandbox 默认 `tenant-default`）。DMP 429/5xx/超时/契约错误一律 fail-closed 为 no-fill；空受众继续 no-ID auction。 |
| I5-D09 | 配置扇出观察 | 实例每秒读取 manifest 并上报 `ssp:runtime:{tenant}:{instance}` 心跳（TTL 30s）与索引；管理 API `/admin/v1/runtime/instances` 只显示已加载/观测版本、ready 与心跳，不显示机密。落后 10s 以上 `readyz` 失败但继续追赶。 |
| I5-D10 | 控制台边界 | 浏览器只调用 console 的 `/api/ssp/*` 代理；SSP admin bearer token 留在 console 进程。SSP 管理台只展示真实 API 数据，未发布 draft 不显示为生效。 |

## 3. 数据与迁移

新增 `ssp/migrations/000002_ssp_control_plane.up.sql`：

- `publisher`/`property`/`auction_policy`/`placement`/`supply_endpoint`/`dsp_endpoint` 增加 `name`、`published_version` 及缺失的策略/尺寸/来源/限流/凭据列；`supply_endpoint` 补 `updated_at` 以支持统一的 publish 更新。
- 新增 `ssp_published_config`（不可变发布投影）、`ssp_config_manifest`（单调 manifest 版本）、`dsp_endpoint_creative_whitelist`（DSP 同步的静态素材白名单）。
- `ssp_config_outbox` 增加 `next_attempt_at` 与 tenant 维度索引。

已确认：在 MariaDB 10.6 空库按 `000001` → `000002` 正序执行成功，`supply_endpoint` 的 5 个新增列存在；`000002` 回滚成功。

## 4. 任务映射与验收证据

| 任务 | 完成 | 交付物 | 验收证据 |
|---|---|---|---|
| I5-01 | ✅ | `control` 包：Publisher/Property/AuctionPolicy/Placement/Supply/DSP endpoint 列表、创建、乐观锁更新、publish；`ssp_published_config`/outbox/manifest 发布 | 本地 MariaDB：逐级创建并 publish 全部 6 类资源均返回 202；引用未发布资源返回 422 `invalid_reference`；陈旧版本返回 409。 |
| I5-02 | ✅ | `POST /openrtb2/auction`：`X-Supply-Key` 哈希鉴权、property/placement 绑定、尺寸/Origin/站点/币种/限流/TMax 校验 | 本地实测有效凭据 filled、错误凭据 401、站点不匹配 403；入口 deadline 由 `AUCTION_TIMEOUT_MS` 与 `tmax` 取小。 |
| I5-03 | ✅ | `auction.Engine`：并行 fan-out、每 DSP 超时、8 步候选校验、first-price + priority + hash tie-break、no-bid、win/loss notice | `engine_test.go` 覆盖最高价胜出、同价 priority、慢 bidder 隔离、below floor、货币/响应 ID/素材白名单、notice 宏展开；本地 Supply filled。 |
| I5-04 | ✅ | `POST /sdk/v1/ad-requests`、`/sdk/v1/openads-sdk.js`、Portal live 模式、脱敏 diagnostics | 本地 SDK 返回 `filled` + render/click/event token + `auction_id/outcome/duration_ms`；无身份请求 filled；DMP 503 时 no-fill；Portal 仍走同一 Client API。 |
| I5-05 | ✅ | DSP `ext.openads` 素材声明、`POST /internal/v1/dsp-endpoints/{id}/creative-registry:upsert`、只读白名单、AEAD renderer/click/impression | 本地 `creative-registry:upsert` accepted=1，SSP 白名单可查；render 壳 CSP/nonce 正确、asset 200 且字节 SHA-256 匹配、click 302 到 allowlisted landing、impression 先 accepted 后 duplicate。 |
| I5-06 | ✅ | `dmp.Adapter` HMAC 伪名 + fail-closed；segments 仅进入 OpenRTB `user.data` 透传 | `adapter_test.go` 覆盖 namespace 隔离、仅发送 digest、503/超时 fail-closed、无身份不查询；本地 DMP 503 时 SDK no-fill。 |
| I5-07 | ✅ | Valkey manifest 刷新器、实例心跳/索引、`/admin/v1/runtime/instances`、outbox 列表/手动 retry、config 指标 | 本地 `/readyz` 在 manifest v2 后 ready；runtime 显示实例 loaded/observed/ready；outbox 全部 `published_at`；失败事件可 retry。 |
| I5-08 | ✅ | console `/api/ssp/*` 代理 + `console/web/ssp-live.js`：总览、Publisher、Property、Auction Policy、Placement、Supply、DSP、配置发布、实例状态 | `node --check` 通过；console 单测验证 token 注入、仅 `/admin/v1/*` 可转发、非 admin 404、DELETE 405、未配置 token 503。 |

## 5. 验证命令

```bash
# 全模块
for m in identity console ssp dsp dmp; do (cd "$m" && gofmt -l . && go vet ./... && go test ./...); done

# 控制台脚本与 SDK
node --check console/web/ssp-live.js
node --check ssp/internal/ssp/adapter/http/sdk/openads-sdk.js

# 配信全链路（filled → render → impression → click），CI 与本地共用
SSP_ADMIN_TOKEN=... DSP_ADMIN_TOKEN=... ./ssp/scripts/filled-path.sh

# DSP 账务写路径（真实 MariaDB，未设置 DSN 时跳过）
cd dsp && DSP_MARIADB_TEST_DSN="dsp_user:dsp_pass@tcp(127.0.0.1:3306)/dsp_db?parseTime=true" \
  go test ./internal/dsp/lease/ -run TestSettleReservation -v

# 迁移与 compose
make migration-check
docker compose -f deployments/docker-compose.yml config >/dev/null

# 干净库迁移（本地 MariaDB）
# 000001 + 000002 正序执行；000002 回滚
```

本地端到端证据（MariaDB/Valkey + 假 DSP，SSP 端口 8090）：

- `POST /sdk/v1/ad-requests` → `200 filled`，`render_url`/`click_url` 同源，`diagnostics.outcome=bid`。
- `GET /render/v1/{token}` → `200 text/html` 且 CSP `script-src 'nonce-...'`、`frame-ancestors https://publisher.test`。
- `GET /render/v1/assets/{token}` → `200 image/png`，`Cache-Control: no-store`，字节与共享卷文件一致。
- `POST /sdk/v1/impressions` → 首次 `accepted`，重复 `duplicate`。
- `GET /click/v1/{token}` → `302` 到 `https://publisher.test/landing`。
- Supply `POST /openrtb2/auction` → `200` 有效 `seatbid`，`adm` 仅引用 SSP render URL。
- DMP 不可用 → SDK `no_fill`（fail-closed）；错误 Supply key → 401；站点不匹配 → 403。

## 6. 明确延期与已知限制

1. **账务**：impression 只做 token 校验与进程内去重，不写 `ssp_delivery_event`/payable/DSP billable outbox；Iteration 6 实现唯一事务与冲正。
2. **通知重试**：win/loss notice 当前单次 best-effort，失败只计数与告警；指数退避属 Iteration 6。quarantine 处置 API 曾不可用，已于 2026-11-03 修复（见 §7 F5-03）。
3. **身份与授权**：Supply 使用共享 secret 的哈希鉴权，DSP→SSP 内部同步使用共享 internal bearer；mTLS/workload identity 待 D-01。
4. **多实例**：本地只验收单实例；Valkey manifest/心跳键与落后 10s 摘除逻辑支持多实例，但未在真实多实例拓扑压测。
5. **DMP sandbox 数据**：需要已发布 snapshot 才能返回 segments；没有发布数据时 SSP 按设计 fail-closed。DMP tenant 绑定通过 `SSP_DMP_TENANT_ID` 适配（默认 `tenant-default`）。
6. **素材同步**：DSP 既有 published creative 必须在本版本后重新 publish，才会包含 `asset_ref`、`asset_sha256`、尺寸与 `landing_url`；同步前 SSP 拒绝其 bid。
7. **控制台**：SSP 管理台复用 console 的模拟样式，只覆盖 I5 范围资源；DSP “同步到 SSP” 仍通过 DSP 管理 API 触发（`POST /admin/v1/ssp/creative-registry/sync`，sandbox 可在 body 传 `dsp_endpoint_id`）。
8. **限流**：进程内 token bucket，不跨实例共享；跨实例配额后续进入网关/Redis 方案。


## 7. 修复记录（2026-11-03，Iteration 6 前置）

iteration-6 就绪度评审在 main（合并提交 `6d29702`）上复测配信链路后，发现 2 个 P0、3 个 P1 缺陷正好落在 Iteration 6 的验收路径上。本节记录修复内容、验证证据与残留限制；这些问题**不属于** I5 原始验收范围，但若不修复，I6-02/03/04/05 无法通过。

### 7.1 缺陷与修复

| ID | 级别 | 问题 | 修复 | 验证证据 |
|---|---|---|---|---|
| F5-01 | P0 | DSP 创建了 `control.SQLStore` 但从未调用 `SetSettlementStore`，运行态 `settlementStore` 恒为 nil。所有 `nurl`/`lurl`/`burl` 返回成功却**不写任何持久记录**：无 settlement 行、不释放预算、无 advertiser spend。 | [dsp/cmd/dsp/main.go](../../../dsp/cmd/dsp/main.go) 用同一 `*sql.DB` 构造 `lease.NewSQLBudgetStore` 并注入；DB 不可用时记录 warn 而非静默跳过。 | notice 处理路径从"204 空转"变为真实事务；见 F5-02 的 MariaDB 断言。 |
| F5-02 | P0 | `advertiser_spend_ledger` 的 INSERT 只列 6 列，缺 `advertiser_id`/`campaign_id`/`entry_type`/`currency`/`occurred_at`/`recorded_at` 六个 NOT NULL 列，真实执行必然 `ERROR 1364`；且使用 `ON DUPLICATE KEY UPDATE amount_micros = ...` 改写追加式账本。 | [sql_allocator.go](../../../dsp/internal/dsp/lease/sql_allocator.go)：归属改为从 `line_item JOIN campaign` 权威解析（不信任 notice body，避免伪造通知串账）；补齐全部必填列；upsert 改为 `entry_id = entry_id` 的幂等空更新，账本金额不可变。 | 新增 [settle_integration_test.go](../../../dsp/internal/dsp/lease/settle_integration_test.go)（真实 MariaDB，`DSP_MARIADB_TEST_DSN` 门控）：billable 结算后 spend=2500、advertiser/campaign/line_item 三者与 seed 一致、`entry_type=billable`、delivery event 1 条、settlement 1 条；同终态重放幂等且仍为 1 条；`spent_micros=2500 / leased_micros=0`；冲突终态返回 `ErrReservationConflict` 并写入 `pending` quarantine。 |
| F5-03 | P1 | `POST /admin/v1/quarantine/{id}/resolve` 写 `status='resolved'`，但表约束只允许 `('pending','accepted','rejected')`，接口恒 500；`resolved_at`/`resolved_by`/`resolution_reason` 三列从未写入。 | 新增 [000004_dsp_quarantine_resolution.up.sql](../../../dsp/migrations/000004_dsp_quarantine_resolution.up.sql)（MariaDB 无法 DROP 内联 CHECK，改用 `MODIFY COLUMN` 去掉后重建具名约束）并同步 [sql_store.go](../../../dsp/internal/dsp/control/sql_store.go)：写入 `resolved_at`/`resolution_reason`，重复处置幂等、未知 ID 404，列表接口回显这两列。 | 迁移 up/down 往返均成功；实机 `POST .../resolve` → 200 `{"status":"resolved"}`，重放 200，未知 ID 404；`GET /admin/v1/quarantine` 返回 `resolved_at` 与 `resolution_reason`。 |
| F5-04 | P1 | SSP 出站通知沿用默认重定向策略（allowlist 主机可 302 到内网）；`CallbackAllowed` 只比对 hostname 字符串，不校验 scheme、不拒绝私网地址，与 I0 威胁模型不符。 | 新增 [netguard](../../../ssp/internal/ssp/netguard/netguard.go)：拒绝 loopback/私网/link-local/CGNAT/multicast（含 DNS 解析后校验）；[engine.go](../../../ssp/internal/ssp/auction/engine.go) 的竞价与 notice 客户端统一**禁止跟随重定向**；`CallbackAllowed` 增加 scheme 白名单（仅 http/https）。因沙箱所有 peer 都在私有容器网络，新增 `SSP_ALLOW_PRIVATE_CALLBACKS` 显式豁免（默认关闭，compose 与 CI 显式开启）。 | `netguard` 单测覆盖 14 个 IP 样本（11 拒绝 + 3 放行）、受限 dial 与豁免分支；`CallbackAllowed` 单测覆盖 `ftp:`/`javascript:`/`file:` 与 allowlist 外主机；SSP 全量单测通过。 |
| F5-05 | P1 | CI 只断言 Supply `nbr=2` 与 SDK `no_fill`，**从未验证 filled → render → impression 链路**；DSP 账务 SQL 写路径没有任何集成测试（这正是 F5-02 长期潜伏的原因）。 | 新增 [filled-path.sh](../../../ssp/scripts/filled-path.sh)：发布 DSP advertiser→campaign→creative（1×1 PNG 上传 + SHA-256）→line item，发布 SSP supply chain，同步 creative registry，再断言 filled、render 壳、asset 字节哈希、impression `accepted`/`duplicate`、click 302、Supply seatbid；接入 CI，并把 DSP 启动补齐 admin/DB/Valkey/lease key/资产卷/内部同步等环境变量，新增 settlement 集成测试步骤与失败日志上传。 | 本地实测全绿（CI 步骤已接入，首次远端运行需在推送后确认）；`open_ads_ssp_auctions_filled_total` 由 0 变正，`notices_sent_total=7`。 |

### 7.2 顺带修复的沙箱阻塞项

| 项 | 问题 | 修复 |
|---|---|---|
| 通知回传不可达 | `DSP_BASE_NOTICE_URL` 未配置，nurl/burl 指向 DSP 自身 loopback；SSP 跨容器回传必然失败。同时 bid 的 callback host（`127.0.0.1`）与 SSP endpoint allowlist（`dsp`）不一致，导致整条 bid 被 `creative_not_whitelisted` 丢弃。 | [docker-compose.yml](../../../deployments/docker-compose.yml) 设置 `DSP_BASE_NOTICE_URL: "http://dsp:8081"`，与 callback allowlist 对齐。 |
| 空卷迁移缺失 | [02-apply-migrations.sql](../../../deployments/mariadb-init/02-apply-migrations.sql) 只应用 DSP `000001`，全新卷缺少 `000002`/`000003`/`000004`，DSP 起来即失效。 | 补齐 DSP `000002`–`000004`，并把这些文件加入 `make migration-check` 断言。 |

### 7.3 复验命令

```bash
# 全模块
for m in identity console ssp dsp dmp; do (cd "$m" && gofmt -l . && go vet ./... && go test ./...); done

# 账务写路径（真实 MariaDB）
cd dsp && DSP_MARIADB_TEST_DSN="dsp_user:dsp_pass@tcp(127.0.0.1:3306)/dsp_db?parseTime=true" \
  go test ./internal/dsp/lease/ -run TestSettleReservation -v

# 配信全链路（compose 内运行）
SSP_ADMIN_TOKEN=... DSP_ADMIN_TOKEN=... \
SSP_DSP_BID_URL=http://dsp:8081/openrtb2/bid SSP_DSP_CALLBACK_HOST=dsp \
  ./ssp/scripts/filled-path.sh

make migration-check && docker compose -f deployments/docker-compose.yml config >/dev/null
```

### 7.4 残留限制（进入 Iteration 6 时仍需处理）

1. **通知退避与隔离**：SSP 侧 win/loss notice 仍为单次 best-effort；指数退避、4xx 隔离、SSRF 复核与重放属 I6-07（本次已补齐禁止重定向与私网拒绝，未补退避队列）。
2. **账本冲正**：`publisher_payable_ledger`/`advertiser_spend_ledger` 只写入正向 billable 分录；`reversal`/`adjustment` 与累计冲正上限校验属 I6-06。
3. **SSP 侧账务**：impression 仍只做 token 校验与进程内去重，尚未写 `ssp_delivery_event`/payable/billable outbox（I6-02/03）。
4. **多 scope 结算**：`SettleReservation` 在 `LeaseIDs[scope]` 缺失时静默 `continue`，与"所有 scope 全有或全无"的表述不完全一致，需在 I6-04 收敛。
5. **沙箱开关**：`SSP_ALLOW_PRIVATE_CALLBACKS=true` 仅适用于私有容器网络；生产/多租户环境必须保持默认关闭，并按 D-01 切换 mTLS 调用方身份。
