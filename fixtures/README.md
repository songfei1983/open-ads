# Open Ads 接口与协议版本化 Fixtures

本文档记录 Open Ads 项目在 **Iteration 0** 固化的标准测试数据集（Golden Fixtures）。
所有核心应用（SSP、DSP、DMP）以及集成测试、契约测试均基于本目录下的样本数据执行测试验证。

## 目录结构

```text
fixtures/
├── openrtb2/                    # OpenRTB 2.6 标准竞价往返
│   ├── bid_request_banner.json  # 标准 Banner 竞价请求（含 site, imp, user, device）
│   ├── bid_response_banner.json # 标准有效竞价响应（含 seatbid, price, nurl, burl, lurl, adm）
│   └── bid_response_nobid.json  # 标准 No-Bid 响应（包含 nbr 错误原因码）
├── client_api/                  # 浏览器 / JS SDK / Portal 交互契约 (/sdk/v1)
│   ├── ad_request.json          # 客户端广告请求 (/sdk/v1/ad-requests)
│   ├── ad_response_filled.json  # 填充响应（含受控 render_url, click_url, event_token）
│   ├── ad_response_nofill.json  # 无填充响应
│   ├── identity_optin_request.json  # 身份授权登记 (/sdk/v1/identity)
│   └── identity_optin_response.json # 身份授权凭据与过期时间
├── callbacks/                   # 内部与通知回调协议
│   ├── win_notice.json          # nurl 获胜通知（非计费）
│   ├── loss_notice.json         # lurl 竞价失败通知（释放 reservation 预留）
│   └── billable_notice.json     # burl 计费通知（触发 MariaDB 事务记账与扣款）
├── dmp/                         # DMP 受众与解析协议 (/internal/v1)
│   ├── audience_resolve_request.json  # 受众解析请求（带 HMAC token 与目的上下文）
│   └── audience_resolve_response.json # 匹配受众段响应（不可变快照版本）
└── errors/                      # 自有 JSON API 标准错误信封
    ├── invalid_request.json     # 400 校验错误
    └── version_conflict.json    # 409 乐观锁冲突
```

## 字段规范

1. **唯一标识符**：统一采用 UUID v7（32 位全小写 16 进制字符串）。
2. **金额与货币**：OpenRTB 报文中使用十进制浮点表示 CPM 单价；在内部系统、数据库和回调报文中一律使用整数微单位 `*_micros`（`1 主币 = 1,000,000 micros`，字段声明为 `BIGINT SIGNED`）。
3. **时间格式**：严格使用 ISO 8601 / RFC 3339 UTC 格式（以 `Z` 结尾）。
