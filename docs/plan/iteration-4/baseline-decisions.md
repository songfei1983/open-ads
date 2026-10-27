# Iteration 4 基线决策与架构映射记录

状态：I4-01 完成；I4-02~I4-08 未开始
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
| I4-02 | 未开始 | Snapshot 导入任务 + tokenization adapter + 完整性核验 | 拒绝原始身份输入；行数/checksum/重复率失败不替换旧版本；重跑幂等 |
| I4-03 | 未开始 | Snapshot 发布指针 + Audience Resolve 精确键查询 | 未发布 snapshot 不可解析；固定 snapshot ID；结果数上限且不回显 token |
| I4-04 | 未开始 | Valkey deny gate、epoch、in-flight admission/finalize、tombstone、状态 API | 撤回阻止新 admission；在途响应复核；不确定状态 fail-closed |
| I4-05 | 未开始 | gate 重建、ClickHouse 故障、节点重启与撤回恢复演练 | 重建一致前不 ready；恢复期间无越权受众响应 |
| I4-06 | 未开始 | 保留策略、物理删除与备份到期自动化 | 期限合规；单 snapshot 删除不误删共享分区数据 |
| I4-07 | 未开始 | DMP 独立集成、负载与物理删除 POC 复测 | D-10 结论 |
| I4-08 | 未开始 | DMP 管理台（Provider/Segment/Snapshot/撤回状态） | 导入校验状态清晰；发布需显式确认；UI 不接收原始身份/token |

## 5. 验收证据（2026-10-27）

构建与自动化测试：
- `cd dmp && gofmt -l . && go vet ./... && go test -race ./...` 通过；`console`、`ssp`、`dsp` 未受影响（build/test 通过）。
- ClickHouse 集成测试（`DMP_CLICKHOUSE_TEST_ADDR=127.0.0.1:9000`）覆盖创建、重复创建、引用缺失、不可变字段、乐观锁冲突、当前版本读取与审计内容/顺序。

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
