# 设计文档修订说明

本次修订对应审查报告的编号。所有改动均在 README / HLD / LLD / API 中完成，未改变文档职责分层。

## P0

| # | 问题 | 修改位置 |
|---|---|---|
| 1 | 配置刷新无法扇出到多实例 | LLD §2.4.1：拆为 publisher（抢 outbox、写 Valkey、递增 `cfgver`）与 syncer（每实例按版本号同步进程快照、上报 `loaded_cfgver`）；readyz 改按 `cfgver` 判陈旧；LLD §3.5 增加实例版本上报；HLD §4.1 同步 |
| 2 | reservation 释放链路漏洞 / loss_code 错误 | LLD §2.2.0.1 / §2.2.0.3：可关联的丢弃 bid 必须发 lurl（按码表），无法关联的（第 1、2、7 项）走 DSP 侧悬挂额度治理；`loss_code` 改为 102（并要求对照 OpenRTB 2.6 码表核对）；LLD §3.3 第 5、7 条与 HLD §3.2 / §7：grace 公式纳入 reservation TTL、render token TTL、展示延迟，新增悬挂额度指标 |
| 3 | 多预算范围扣留无回滚 | LLD §3.1.2 伪代码改为全成功或回滚；新增不变量 6；README §7.5 与 LLD §5 增加测试场景 |
| 4 | 计费输入依赖 Valkey、outbox 缺字段 | LLD §2.2.0.3 / §2.2.1、API §2.2：金额等信息放入服务端 AEAD event token，不再依赖 Valkey；`ssp_billable_outbox` 增加 `burl_target`、`status`、`last_http_status`、`last_error_sanitized` 及状态流转说明 |
| 5 | render 端点矛盾、CORS 与沙箱冲突 | 拆为 `/render/v1/`（HTML 壳）与 `/asset/v1/`（图片字节）：API §2.2、LLD §3.2.1.3；CORS 对 `null` Origin 的 impressions/events 单独定义：LLD §2.2.2、API §5 |
| 6 | 缺认证授权设计 | 新增 API §1.2（各接口认证方式与租户解析）；LLD §1.5、`supply_endpoint` 增加凭据哈希列；README §4 新增决定 13；SDK 撤回引入 `revoke_token` |
| 7 | subject_token 无法生成 / 密钥轮换撤回 | LLD §2.2.1.1：两步 HMAC 派生、provider 经 tokenization 服务导入、轮换双读窗口；API §2.3 新增 `subject-tokens:derive`；撤回请求携带所有生效 key version 的 token：API §4.1、LLD §4.2 / §4.3、HLD §6 |
| 8 | `site.page` 被改写为 path | `site.page` 恢复为完整 URL；规范化路径放入 `site.ext.openads.page_path`；LLD §3.1 / §3.1.1.1 / §3.1.2 伪代码、HLD §4.2 同步；伪代码补 `/` 根前缀处理 |

## P1

| # | 修改位置 |
|---|---|
| 9 冲正模型 | 两张 ledger 表增加 `entry_type` 枚举与 `reverses_entry_id`（含 CHECK 与自引用外键）；LLD §1.3 允许部分冲正并限制累计冲正；§2.6 冲正生成独立 delivery event |
| 10 隔离对账 | LLD 新增 §3.3.2：`dsp_notice_quarantine` 表、处置规则、每日对账报告；API §3.2 新增隔离查询/处置接口；README §6 增加“迟到或被拒 billable 的损失承担”确认项 |
| 11 Supply 直连路径 | LLD §2.2、API §2.1、README §4.1：直连路径复用同一 render/impression 链路，首期不提供 `burl` |
| 12 预算参数与变更 | LLD 新增 §3.3.1：租约默认参数、预算上调/下调、账户创建；`line_item` 增加 `flight_start_date`/`flight_end_date`；API §3.2 DTO 同步 |
| 13 MariaDB 版本 | LLD 引言：最低 10.6 |
| 14 asset store 例外 | README §1 声明例外并补组件图；LLD §3.2.1.1 增加 GC；registry 增加 `status` |
| 15 缓存 TTL 矛盾 | LLD §2.4.1：配置类 key 不设 TTL |
| 16 tie-break 可操控 | LLD §2.2.0.2：改用 SSP 持有 secret 的 `auction_hash`；AuctionPolicy 取值同步 |
| 17 发布语义 | 新增 API §1.3；LLD §2.4 说明 SSP 无草稿态 |
| 18 ClickHouse 快照回收 | LLD §4.4：`snapshot_month` 取值、回收方式、物理删除时限；README §6 增加确认项 |

## P2

示例 ID 改为 32 位 hex；SDK 示例补 `diagnostics`；`AssetStore` 改用自定义 `ID`；租户 fixture 描述更正；预算类 DDL 移到独立的 §3.2.2；SSP 表中误放的 DMP 端点已移除；内部路径前缀统一；README 术语表补 Valkey 的安全协调状态；伪代码修正（`else if`、`panic`、USD 硬编码）；`notice_id` 与 `source_event_key` 改为长度前缀编码；API 新增 Idempotency-Key 语义（§1.4）、错误码目录（§1.5）、机读契约要求（§1.6）。

## 需要你确认的点

- OpenRTB loss reason code 的具体取值（100 / 102 / 3 / 2）是依据记忆写入的，已在文中标注“实现前对照 2.6 码表核对”。
- 租约默认参数（10 分钟、30%、10% 悬挂阈值等）只是起点，已标注需压测校准。
- tokenization 方案（provider 通过 SSP 的 `subject-tokens:derive` 获取 token）是新增的设计决定，需要架构评审。
- 迟到/被拒 billable 由谁承担，属于业务决策，仅在 README §6 列为确认项，没有替你选定。
