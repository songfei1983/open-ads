# Iteration 4 基线决策与架构映射记录

状态：I4-01~I4-08 完成（sandbox 拓扑，D-10 生产拓扑复测待 D-01）
适用分支：`feat/iteration-4`
关联文档：[Iteration 3 基线决策](../iteration-3/baseline-decisions.md) | [首期开发计划](../implementation-plan.md) | [API](../../design/API.md) | [LLD](../../design/LLD.md)

## 1. 目标与范围

Iteration 4 聚焦 DMP 独立子系统与隐私门禁：先把 Provider/namespace/Segment 定义为可版本化、可审计的控制面资源，再依次完成 Snapshot 导入与校验、不可变发布指针与解析、Valkey deny gate 与撤回、恢复演练、保留/物理删除和管理台。本迭代不实现真实身份解析生产流量，也不改变 SSP/DSP 现有热路径。

## 2. 基线决策

| 决策 ID | 事项 | 基线 |
|---|---|---|
| I4-D01 | 控制面存储 | DMP 不引入 MariaDB。Provider、namespace、segment 定义与审计记录都写入 ClickHouse；定义表为追加写，当前版本取 `max(version)`，同版本并列时按 `revision_id` 最小者胜出，读路径始终可确定唯一当前值。 |
| I4-D02 | 乐观锁 | 客户端提交 `version`；与当前版本不一致返回 `409 version_conflict`。ClickHouse 无多语句事务，更新实现为"追加下一版本 + 回读校验"：并发写同一版本时后写者判定为冲突并返回 409，其行只作为审计保留，不覆盖优胜版本。 |
| I4-D03 | 租户授权 | 管理 API 用 bearer token 且 token 绑定单一 tenant（`DMP_ADMIN_TOKEN` + `DMP_ADMIN_TENANT_ID`）。请求体不接受 `tenant_id`（未知字段返回 400），存储层固定按配置租户过滤，因此 token 无法跨租户读写。 |
| I4-D04 | 审计 | 每次变更尝试都写 `dmp_control_audit`：accepted / conflict / not_found / rejected，包含 actor、action、资源、版本、request_id 与脱敏说明。actor 使用配置的操作者标识，不记录 bearer token、subject token 或原始身份。 |
| I4-D05 | 定义字段规则 | ID 为小写 slug（`[a-z0-9][a-z0-9._-]{0,63}`）；namespace 仅支持 `hmac-sha256`；`retention_months` ≤ 3（已确认的身份映射保留上限）；状态仅 `active`/`retired`；`provider.namespace` 与 `segment.provider_id` 创建后不可变；新引用只能指向 active 定义。 |
| I4-D06 | 管理面超时 | 管理 API 使用独立 10 秒预算，不套用 bid 热路径的 15 ms deadline，避免 ClickHouse 写入被热路径超时中断。 |
| I4-D07 | fail-closed 默认 | 未配置 token、未挂载 store 或 ClickHouse 不可达时，管理 API 统一返回 503，绝不暴露匿名或半可用写入。 |

## 3. 当前起点

- DMP 已有 `/healthz`、`/readyz`、`/metrics`，以及 `/internal/v1/audiences:resolve`（sandbox stub）与 `/internal/v1/identity:revoke`（明确 `501`，不谎报完成）。
- ClickHouse 迁移 000001 定义了 membership snapshot、publication、segment definition、撤回与事件表；DMP 此前没有连接 ClickHouse 的代码。
- SSP 已按 `audience:resolve` 契约调用 DMP，fixtures 固定 `fixtures/dmp/audience_resolve_*.json`。
- 本次新增 000002 迁移与控制面包（`dmp/internal/dmp/control`）：namespace/provider/segment 版本化 API、审计查询、ClickHouse store 与集成测试。

## 4. 任务映射

| 任务 | 完成 | 下一步交付物 | 验收重点 |
|---|---|---|---|
| I4-01 | ✅ 完成（已验收） | — | 已实现 `GET/POST /admin/v1/namespaces`、`/providers`、`/segments` 与对应 `PUT`，以及 `GET /admin/v1/audit`。每次写入追加不可变版本行（`dmp_namespace_version`、`dmp_provider_version`、`dmp_segment_definition_version` 增加 `revision_id`/`description`），`version` 不匹配返回 `409 version_conflict`，重复创建返回 `409 resource_conflict`，引用非 active 或缺失返回 `422 invalid_definition`，不可变字段变更返回 `422 immutable_field`。bearer token 绑定单租户，请求体携带 `tenant_id` 被 400 拒绝。所有变更尝试（含校验拒绝，带尝试的资源 ID）写 `dmp_control_audit`。未知路径/不支持方法返回 JSON `404 not_found` / `405 method_not_allowed`；存储不可用返回 `503 control_plane_unavailable`。 |
| I4-02 | ✅ 完成 | — | 新增 `POST/GET /internal/v1/snapshots`、`POST /internal/v1/snapshots/{id}/rows`、`POST /internal/v1/snapshots/{id}/verify`（`DMP_INTERNAL_TOKEN` 保护，未配置 503）。导入只接受 64 位小写 hex 的伪名 token，邮箱/手机号/设备 ID 等原始身份在写入前即被拒绝；segment 必须是该 provider 的 active 定义；批次内重复计为 duplicate、已存在行按幂等跳过；`Verify` 重算 distinct 行数、规范 SHA-256 checksum 与重复率（≤5%），任一不符则 job 置 `failed` 并停止接收行，已发布快照不受影响。批次计数写入独立 batch 表，避免并发追加丢计数。 |
| I4-03 | ✅ 完成 | — | 新增 `POST /internal/v1/snapshots/{id}/publish` 与真实 `POST /internal/v1/audiences:resolve`。发布前重新校验 stored rows 与 checksum（与 verify 结果一致才写入 `dmp_snapshot_publication`），重复发布幂等，并能在"marker 已写但状态追加失败"时自动对账；当前发布指针按 `published_at` 取最新。解析默认只查当前发布快照，携带 `snapshot_id` 时仅当该快照已发布才可命中（verified 但未发布 → 503 `no_published_snapshot`），结果按 `max_segments`（默认 10、上限 100）截断，只返回 segment ID，不回显 token。 |
| I4-04 | ✅ 完成 | — | 新增独立 Valkey deny gate（`dmp:<tenant>:gate:*`，仅 `dmp:*` ACL）：epoch 校验 + Lua 原子 admission 登记有界 in-flight lease（2s TTL）、`finalize` 复核后再释放、`deny` 原子写入主体级 deny key 并返回当时在途数；解析在 admission 前阻断已撤回主体（返回空受众、不查存储），finalize 复核失败时丢弃已解析 segments，gate 不可用/epoch 丢失返回 `503 policy_state_unavailable`。新增 `POST /internal/v1/identity:revoke`（tombstone 写 `dmp_identity_revocation`，状态追加 `dmp_identity_revocation_status`，等待 ≤1s drain 后返回 `completed`/`pending`，tombstone 失败仍保留 gate 并返回 pending）与 `GET /internal/v1/identity-revocations/{id}`、`GET /internal/v1/identity-revocations`。启动时若 epoch 缺失，从 ClickHouse tombstone 重建 deny key 后才发布 epoch（重建期间 admission 一律 fail-closed）。 |
| I4-05 | ✅ 完成 | — | readiness 改为依赖检查：每 2s 校验 ClickHouse 可达性并调用 `gate.Recover`（epoch 缺失时先从 ClickHouse tombstone 重建 deny key、再发布 epoch），检查失败期间 `/readyz` 返回 503 并带 `reason`；epoch 丢失无需重启即可自动重建，重建窗口内所有 admission 仍 fail-closed。新增 `dmp/scripts/gate-drills.sh` 覆盖 5 组演练：epoch 丢失在线重建、ClickHouse 停机（拒绝解析且 deny 保留）、恢复后回升、节点重启后撤回保持、在途 lease 统计与立即阻断。 |
| I4-06 | ✅ 完成 | — | 保留策略在配置层强制已批准上限（membership ≤92 天、事件 ≤365 天、物理删除 SLA ≤30 天，违反即启动失败），DMP 事件表以 `TTL event_date + 365 DAY` 自动过期。`POST /internal/v1/snapshots/{id}/delete` 通过 `ALTER TABLE ... DELETE WHERE tenant_id + snapshot_id SETTINGS mutations_sync=1` 删除单个快照，拒绝删除当前服务快照（409），删除后复核行数并置 job `retired`；因过滤条件只含 snapshot_id，同一月分区中的其他快照不受影响。`revocation.PurgeSubject` 删除该主体的 membership 与事件行并复核归零，保留 tombstone 作为拒绝依据。后台 retention worker（默认 15 分钟）删除过期且非服务的快照、在 grace（沙箱 0 分钟）后完成待处理物理删除并追加 `completed` 状态，SLA 超期与失败写入报告；`POST /internal/v1/retention:run` 可手工触发，`GET /internal/v1/retention:policy` 暴露生效策略与上限。备份到期由 `dmp/scripts/backup-expiry.sh`（默认 dry-run，`--apply` 删除，强制 ≤90 天）与 `make backup-expiry` 承担。 |
| I4-07 | ✅ 完成（sandbox 拓扑） | 生产拓扑定案后按 D-01 复测 | 新增 `dmp/scripts/integration.sh`（仅 clickhouse+valkey+dmp 的独立集成：探针、鉴权、导入拒绝原始身份、校验、发布、解析、撤回、保留 sweep、物理删除完成、服务快照禁止删除，全部 PASS）与 `dmp/scripts/load_poc.py`（经真实导入 API 灌数、发布、并发解析、按快照删除并验证隔离）。`dmp/scripts/gate-drills.sh` 改为自播种，避免受历史数据影响。 |
| I4-08 | ✅ 完成 | — | console 新增 `/api/dmp/*` 反向代理（服务端注入 admin token、仅 GET/POST/PUT、路径白名单为 `/admin/v1/` 与 `/internal/v1/{snapshots,identity,retention,publications}`，非白名单 404、未配置 503），浏览器不接触任何凭据；`dmp.html` 改由 `dmp-live.js` 驱动，实现总览（Provider/Segment/快照/撤回计数、当前服务快照标记）、Provider 与 Segment 版本化编辑、Snapshot 导入（JSON 成员行 → 自动校验）/校验/发布（显式确认，展示快照/行数/状态）/删除（不可回滚确认）、撤回登记（客户端强制 64 位小写 hex，拒绝原始身份）与状态列表、保留策略与手工 sweep。DMP 内部 API 允许 sandbox 操作员 token 驱动导入/发布/撤回，适配器 token 仍保留给服务间调用。新增 `GET /internal/v1/publications` 供控制台标注当前服务快照。 |

## 5. 验收证据（2026-10-27）

构建与自动化测试：
- `cd dmp && gofmt -l . && go vet ./... && go test -race ./...` 通过；`console`、`ssp`、`dsp` 未受影响（build/test 通过）。
- ClickHouse 集成测试（`DMP_CLICKHOUSE_TEST_ADDR=127.0.0.1:9000`）覆盖创建、重复创建、引用缺失、不可变字段、乐观锁冲突、当前版本读取与审计内容/顺序。

I4-02 实测：无 token 401；创建 job 201；`alice@example.com` 被 400 拒绝；两行三会员 append 存 3 行；重复投递 `stored_rows=0, idempotent_rows=2`；verify 200 `verified`（3 行、重复率 0）；错误 checksum 的 job verify 422 `verification_failed` 且 `job.status=failed`，此后追加 409；列表同时展示 verified/failed 状态。

I4-03 实测：verified 未发布快照的固定 ID 查询 503；publish 200 且 job 转 `published`；当前解析返回该快照与 segment；已发布快照固定 ID 仍可解析；未知主体返回空数组；`max_segments=101` 400；SSP 形状请求（含 `tenant_id`/`consent_context`）200 且响应不含 token。

I4-04 实测：revoke 202 `completed`（`drained_lookup_count` 反映在途数，注入 1 个 live lease 时报告 1，lease 过期后归零）；撤回后同一主体解析返回空、其他主体不受影响；Valkey 停机 → 503 `policy_state_unavailable`；epoch 丢失 → 503，重启后从 tombstone 重建，已撤回主体仍空、未撤回主体正常；未认证 revoke 401，原始身份 token 400。gate 单测（真实 Valkey）覆盖 admit/deny/finalize/重建/epoch 丢失。

I4-05 演练（`./dmp/scripts/gate-drills.sh`，全部 PASS）：epoch 丢失瞬间解析 503 且无受众泄漏、2s 内 readiness 自动恢复、重启后已撤回主体仍为空；ClickHouse 停机时 readiness 503、解析 503、Valkey deny key 保留，恢复后重新 ready 且撤回仍生效；DMP 节点重启后撤回保持；注入在途 lease 后撤回报告 1 并立即阻断。修复演练暴露的缺陷：`/internal/v1/audiences:resolve` 曾误用 15ms bid-path 超时导致 ClickHouse/Valkey 偶发超时，现内部 API 全部使用 10s 管理预算。

I4-06 实测：删除当前服务快照 409；删除非服务快照返回 `deleted_rows=1`，同月另一快照仍可解析；未发布快照固定 ID 解析 503；`retention:policy` 返回 90/365/30 天与上限；撤回后 sweep 报告 `purged_subjects=1`、状态转 `completed`，该主体 membership 行数归零且解析仍为空；`dmp_event` 已带 365 天 TTL。沙箱无备份目标目录，`backup-expiry.sh` 以 dry-run 为默认并在 `DMP_BACKUP_DIR` 缺失时明确失败。

I4-07 sandbox 拓扑实测（最新一次）：100,000 membership 行导入 2.96s（≈33.8k rows/s），verify 0.07s；20 并发 600 次解析 793 req/s，p50 25.2ms / p95 28.2ms / p99 30.2ms（目标 p95 ≤50ms）；删除同规模快照 0.18s（目标 ≤60s），删除后服务快照仍正确解析。**D-10 结论（限定）**：在单节点 sandbox（MariaDB 无关、ClickHouse/Valkey 本机容器、100k 行）上解析与物理删除均达标；结论不外推到生产拓扑，D-01 定案后需按真实副本/分区/规模复测。演练期间发现并修复两处性能相关问题：Valkey 每命令重建连接（改为有界连接池）与每次 admission 重复读 epoch（改为 1s 缓存，mismatch 即失效）。

I4-08 实测（浏览器）：控制台各页无 console/page error；代理白名单外路径 404、未配置 token 503、DELETE 405；UI 导入 2 行快照后自动校验通过并出现"已校验"行；发布确认弹窗展示快照/行数/状态，确认后当前服务标记移动到新快照（`/internal/v1/publications` 同步）；撤回表单对 `alice@example.com` 直接拒绝并提示"必须是 64 位小写 hex"，合法 token 撤回返回 `completed` 且列表新增记录；保留页展示 90/365/30 天与上限，手工 sweep 提示"物理删除 1 个主体"并渲染最近一次 sweep 报告；页面从不显示 token 或原始身份。

运行中服务验收（`/admin/v1`，沙箱租户）：
- 认证：无 token / 错误 token → `401 unauthenticated`。
- 正向链路：创建 namespace → provider → segment（`valid_from` 自动填充）→ 三次 `PUT` 递增版本，均 `201/200`。
- 契约拒绝：大写 ID、`retention_months=4`、`hashing_algorithm=sha1` → `422 invalid_definition`；请求体注入 `tenant_id`、非法 JSON、未知字段 → `400 invalid_request`；缺失 `version` → `422` 且消息单行（`invalid definition: version is required`）；`limit=999` → `400`。
- 锁与不可变：陈旧 `version` → `409 version_conflict`；`provider.namespace` / `segment.provider_id` 变更 → `422 immutable_field`；未知资源 → `404 not_found`。
- 并发：同版本 8 个并发 `PUT` → 恰好 1 个 `200`、7 个 `409`，当前定义为胜者内容；ClickHouse 中该版本保留 8 行，读路径按 `revision_id` 最小值稳定选中胜者。
- 引用 fail-closed：retired provider 下新建 segment、retired namespace 下新建 provider 均 `422`。
- 路由信封：`/admin/v1`、`/admin/v1/`、未知集合 → JSON `404`；`DELETE` 集合/成员 → JSON `405` + `Allow`。
- 存储故障：停止 ClickHouse 后读写均 `503 control_plane_unavailable`；恢复 ClickHouse 后无需重启服务即回到 `200/201`。
- 审计：accepted/conflict/not_found/rejected 全部入库，`request_id` 与响应一致；校验拒绝记录携带尝试的资源 ID；ClickHouse 与容器日志中均无 bearer token（grep 计数为 0）。

## 6. 风险与未决

1. ClickHouse 无事务：版本行与审计行不是原子写。变更路径的审计写失败映射为 `503`，已写入的版本行保留，重试会得到 409，需要按审计缺失人工核对；校验拒绝路径的审计失败只记录告警并保留 422，存在少量未审计的拒绝记录。
2. 同版本并发写会保留多行（只有 `revision_id` 最小者为胜者），版本表会积累落败行；需要时可后续以受控 mutation 清理同一版本的落败行。
3. D-10 未关闭：精确 subject 查询与物理删除的拓扑级目标仍待 I4-07 POC 复测。
4. 管理面认证仍是共享 bearer token（sandbox）。生产态需按 D-01 切换为 mTLS workload identity + 按调用方授权。
5. I4-02 起必须在写入前拒绝原始身份；当前控制面只接受定义元数据，不接触 subject token。
6. sandbox 阶段单租户：`audiences:resolve` 现在强制 `tenant_id` 且必须是数据面绑定租户（`tenant-default` 为过渡别名），但调用方身份仍未认证；D-01 关闭前必须换成 mTLS 调用方授权，让租户来自凭据而非请求体。

## 7. 风险收敛提案与进度

| 提案 | 内容 | 状态 |
|---|---|---|
| R7 | 快照发布即封版：`verify` 对已发布快照返回 `409 snapshot_sealed`，`publish` 保持幂等；撤回 purge 写入 `dmp_snapshot_purge` 台账（`purged_rows`），控制台显示"已撤回 N"并隐藏封版快照的校验/删除动作，消除 purge 后的 checksum 假失败 | ✅ 完成 |
| R8 | retention worker 用 Valkey 租约选主，避免多副本重复 sweep | ✅ 完成 |
| R4+R6 | 引入 `Principal{Caller,Tenant,Scopes}` 与 `Authenticator`（Bearer 现状 + 预留 mTLS），resolve 强制 `tenant_id`（缺失 400、跨租户 403，过渡期保留 `tenant-default` 别名） | 待办 |
| R1 | 控制面审计改为 intent + outcome 两行，缺 outcome 即"结果未知"并提供巡检查询；拒绝路径审计失败改为 fail-closed | ✅ 完成 |
| R9 | DMP 业务指标：resolve 结果/时延、gate admission、导入校验、保留删除、epoch、tombstone 数 | ✅ 完成 |
| R2 | 同版本落败行巡检清理（保留 `argMin(revision_id)`，先 dry-run 计数） | ✅ 完成 |
| R5 | 抽出共享 `identity.TokenPolicy` + 负例 fixture 集 + 疑似原始身份指标 | 待办 |
| R3 | 负载/POC 参数化并归档报告，D-01 后按生产拓扑复测 | 待办 |
| R10 | DMP OpenAPI 3.1 契约工件 + import/revoke 幂等键 | 待办 |

R2 验收（2026-10-27）：新增 `control.Maintenance`（可选接口，不改动 `Store`）：`GET /admin/v1/maintenance/duplicate-versions` 只统计同版本多行（`GROUP BY id, version HAVING count() > 1`），`POST` 默认 `dry_run=true`、显式 `{"dry_run":false}` 才删除；删除语句 `revision_id != argMin(revision_id)` 且 `mutations_sync=1`，保证返回时存储已修复。控制台「保留策略」页新增"同版本落败行"卡片（巡检/清理两个动作）。live 验证：历史并发写入留下 8 行 v2，dry run 报 7 行、清理后巡检为空且当前版本仍为 2；随后并发写 6 次复现 6 行 → 控制台一步清理 5 行、toast 与卡片均正确。ClickHouse 集成测试覆盖 dry-run 不删除、清理只删落败行、当前版本不变。

R9 验收（2026-10-27）：`observability.Business` 提供有界标签的业务计数/仪表，`/metrics` 在 HTTP 指标之后输出：`dmp_audience_resolve_total{result=resolved|empty|denied|no_publication|unavailable|error}`、resolve 时延直方图（0.005~0.25s，累积）、`dmp_audience_resolve_segments_total`、`dmp_gate_admissions_total{result=admitted|denied|unavailable|finalize_denied|finalize_unavailable}`、`dmp_gate_denied_subjects_total`、`dmp_gate_epoch`、`dmp_snapshot_imported_rows_total`、`dmp_snapshot_verify_total{result}`、`dmp_snapshot_publish_total{result}`、`dmp_revocation_subjects_total`（撤回转 tombstone 的主体数）、`dmp_retention_{sweeps_total,deleted_rows_total,purged_subjects_total,pending_purges}`。live 验证：integration.sh 后 `/metrics` 同时出现 resolved/denied 两种结果、admitted/denied 两种 admission、epoch=1、import 2 行、purge 1 个主体、直方图 count=2。

R1 验收（2026-10-27）：迁移 `000006` 为 `dmp_control_audit` 增加 `phase`（`intent`/`outcome`）与 `intent_id`；每次定义写入先写 intent 行（写不进去就不做写入），完成后写同 `intent_id` 的 outcome 行，崩溃或断连留下的 intent 即"结果未知"。新增 `GET /admin/v1/audit/unfinished`（默认 2 分钟宽限）列出这些尝试；拒绝路径审计失败不再只记日志，改为 `503 control_plane_unavailable`。live 验证：创建命名空间后审计出现成对的 intent/outcome 行，`unfinished` 为 `[]`；注入一条 10 分钟前的孤立 intent 后该接口返回它。ClickHouse 集成测试断言 4 次尝试 = 4 intent + 4 outcome。

R4+R6 验收（2026-10-27）：新增 `internal/dmp/authn`：`Principal{Caller,Tenant,Scopes}`、`Authenticator`、常量时间 `BearerAuth`（空 token 环境变量不会成为凭据）、预留 `MTLS`（按已校验链的 CN 映射身份）。内部 API 每个路由显式要求 scope（`snapshots:read|write`、`identity:revoke`、`retention:run`），缺 scope 返回 `403 insufficient_scope`，凭据与数据面租户不符返回 `403 tenant_mismatch`；resolve 必须携带 `tenant_id`（缺失 `400 tenant_required`、跨租户 `403 tenant_mismatch`），过渡期 `tenant-default` 解析到数据面租户，未绑定租户的数据面直接 `503 internal_api_unconfigured`。live 验证：缺 tenant 400 / 跨租户 403 / 别名 200，integration.sh 与 gate-drills.sh 全 PASS，POC 200 请求 0 错误。

R8 验收（2026-10-27）：worker 通过 `retention.Locker` 在 sweep 前 `SET NX PX` 取租约（`dmp:<tenant>:retention:lease`，10 分钟），未取到返回 `ErrLeaderHeld` → `409 retention_in_progress`；释放用 compare-and-del Lua，过期旧持有者不会误删新租约；手工占住租约时 live sweep 返回 409 且租约值保持不变，释放后 200。单测覆盖串行化与陈旧释放，Valkey 集成测试覆盖互斥/陈旧释放/复用。

R7 验收（2026-10-27）：发布后 purge 1 行 → job `stored_rows=0 / purged_rows=1`；`verify` 返回 409 且状态仍为 `published`；`publish` 仍 200；该主体解析为空；ClickHouse 集成测试覆盖"发布 → purge → 封版 → 幂等发布 → 不再解析"。
