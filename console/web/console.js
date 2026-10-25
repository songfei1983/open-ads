const serviceKey = document.body.dataset.service;
const app = document.querySelector("#app");
const serviceConfigs = {
  ssp: {
    name: "SSP 供给平台",
    shortName: "SSP",
    initials: "S",
    description: "管理供给资源、拍卖策略与下游竞价连接。",
    nav: [
      ["dashboard", "运营总览", "⌂"],
      ["publishers", "Publisher", "◉"],
      ["placements", "广告位 Placement", "▦"],
      ["supply", "Supply 接入", "⇥"],
      ["dsp-endpoints", "DSP 连接", "⇄"],
      ["config", "配置发布", "↻"]
    ],
    stats: [
      ["今日广告请求", "2.84M", "入口流量 · 峰值目标 3,000 QPS", "↗"],
      ["填充率", "68.4%", "较昨日 +2.1%", "◒"],
      ["活跃广告位", "128", "已发布配置", "▦"],
      ["DSP 平均响应", "42 ms", "P95 · 目标 ≤100 ms", "◷"]
    ],
    activities: [
      ["配置已发布", "Placement · Home Leaderboard", "2 分钟前"],
      ["DSP 接入恢复", "DSP North · endpoint-01", "18 分钟前"],
      ["新 Publisher", "Publisher Sakura Media", "1 小时前"]
    ],
    resources: {
      publishers: {
        title: "Publisher",
        description: "管理媒体合作方及其站点/应用资源。",
        noun: "Publisher",
        columns: [["name", "Publisher"], ["property", "媒体资源"], ["requests", "日请求量"], ["status", "状态"]],
        fields: [
          ["name", "Publisher 名称", "text", "例如 Sakura Media"],
          ["property", "主媒体资源", "text", "例如 sakura.example"],
          ["requests", "日请求量", "text", "例如 120K"]
        ],
        rows: [
          { name: "Sakura Media", id: "pub_sakura_01", property: "sakura.example", requests: "842K", status: "active", owner: "Sandbox Tenant", format: "Web", version: "v12" },
          { name: "Northstar News", id: "pub_northstar_02", property: "northstar.test", requests: "516K", status: "active", owner: "Sandbox Tenant", format: "Web", version: "v8" },
          { name: "Greenfield Network", id: "pub_greenfield_03", property: "greenfield.test", requests: "—", status: "paused", owner: "Sandbox Tenant", format: "Web", version: "v4" }
        ]
      },
      placements: {
        title: "广告位 Placement",
        description: "设置尺寸、底价、币种及拍卖策略；仅发布配置进入竞价热路径。",
        noun: "Placement",
        columns: [["name", "广告位"], ["publisher", "Publisher"], ["size", "尺寸"], ["floor", "底价"], ["status", "状态"]],
        fields: [
          ["name", "广告位名称", "text", "例如 Home Leaderboard"],
          ["publisher", "Publisher", "select", [["Sakura Media", "Sakura Media"], ["Northstar News", "Northstar News"]]],
          ["size", "Banner 尺寸", "select", [["728x90", "728 × 90"], ["300x250", "300 × 250"], ["320x50", "320 × 50"]]],
          ["floor", "Floor CPM", "number", "0.50"],
          ["currency", "币种", "select", [["USD", "USD"], ["JPY", "JPY"]]]
        ],
        rows: [
          { name: "Home Leaderboard", id: "plc_home_728x90", publisher: "Sakura Media", size: "728 × 90", floor: "USD 0.50", status: "published", policy: "First-price · at=1", version: "v16" },
          { name: "Article Rectangle", id: "plc_article_300x250", publisher: "Northstar News", size: "300 × 250", floor: "USD 0.35", status: "published", policy: "First-price · at=1", version: "v9" },
          { name: "Mobile Banner", id: "plc_mobile_320x50", publisher: "Sakura Media", size: "320 × 50", floor: "USD 0.20", status: "draft", policy: "First-price · at=1", version: "v2" }
        ]
      },
      supply: {
        title: "Supply 接入",
        description: "登记已认证的 Supply endpoint 与协议 profile。",
        noun: "Supply endpoint",
        columns: [["name", "Supply endpoint"], ["publisher", "Publisher"], ["protocol", "协议"], ["lastSeen", "最近请求"], ["status", "状态"]],
        fields: [
          ["name", "Endpoint 名称", "text", "例如 Sakura Web"],
          ["publisher", "Publisher", "select", [["Sakura Media", "Sakura Media"], ["Northstar News", "Northstar News"]]],
          ["protocol", "OpenRTB Profile", "select", [["2.6", "OpenRTB 2.6"]]],
          ["domain", "接入域名", "text", "例如 exchange.sakura.example"]
        ],
        rows: [
          { name: "Sakura Web", id: "sup_sakura_web", publisher: "Sakura Media", protocol: "OpenRTB 2.6", lastSeen: "刚刚", status: "healthy", auth: "mTLS", version: "v6" },
          { name: "Northstar Exchange", id: "sup_northstar", publisher: "Northstar News", protocol: "OpenRTB 2.6", lastSeen: "1 分钟前", status: "healthy", auth: "mTLS", version: "v3" },
          { name: "Legacy Preview", id: "sup_legacy_preview", publisher: "Sakura Media", protocol: "OpenRTB 2.6", lastSeen: "昨天", status: "inactive", auth: "mTLS", version: "v1" }
        ]
      },
      "dsp-endpoints": {
        title: "DSP 连接",
        description: "配置竞价地址、超时、优先级和回调 host 白名单。",
        noun: "DSP endpoint",
        columns: [["name", "DSP endpoint"], ["timeout", "超时"], ["priority", "优先级"], ["p95", "响应 P95"], ["status", "状态"]],
        fields: [
          ["name", "DSP 名称", "text", "例如 DSP North"],
          ["url", "Bid URL", "url", "https://dsp.example/openrtb2/bid"],
          ["timeout", "超时", "number", "80"],
          ["priority", "优先级", "number", "10"]
        ],
        rows: [
          { name: "DSP North", id: "dsp_endpoint_north", timeout: "80 ms", priority: "10", p95: "42 ms", status: "healthy", callback: "callbacks.north.test", version: "v7" },
          { name: "DSP Cedar", id: "dsp_endpoint_cedar", timeout: "80 ms", priority: "20", p95: "58 ms", status: "healthy", callback: "bid.cedar.test", version: "v4" },
          { name: "DSP Harbor", id: "dsp_endpoint_harbor", timeout: "60 ms", priority: "30", p95: "—", status: "inactive", callback: "billing.harbor.test", version: "v2" }
        ]
      },
      config: {
        title: "配置发布",
        description: "检查控制面 outbox、热配置发布状态和失败事件。",
        noun: "配置事件",
        columns: [["name", "配置事件"], ["aggregate", "资源"], ["version", "版本"], ["updated", "更新时间"], ["status", "状态"]],
        fields: [],
        rows: [
          { name: "placement.updated", id: "evt_10842", aggregate: "Home Leaderboard", version: "v16", updated: "2 分钟前", status: "published", attempts: "1 次", manifest: "manifest-892" },
          { name: "dsp_endpoint.updated", id: "evt_10841", aggregate: "DSP North", version: "v7", updated: "18 分钟前", status: "published", attempts: "1 次", manifest: "manifest-891" },
          { name: "placement.updated", id: "evt_10840", aggregate: "Mobile Banner", version: "v2", updated: "32 分钟前", status: "pending", attempts: "5 次", manifest: "等待重试" }
        ]
      }
    }
  },
  dsp: {
    name: "DSP 投放平台",
    shortName: "DSP",
    initials: "D",
    description: "管理广告主、投放活动、创意素材与预算。",
    nav: [
      ["dashboard", "投放总览", "⌂"],
      ["campaigns", "Campaign", "▤"],
      ["line-items", "Line Item", "◫"],
      ["creatives", "Creative 素材", "▧"],
      ["budgets", "预算与账本", "＄"],
      ["quarantine", "账务隔离", "⚑"]
    ],
    stats: [
      ["今日展示", "1.92M", "Sandbox 测试流量", "◩"],
      ["活跃 Campaign", "24", "已发布配置", "▤"],
      ["今日消耗", "$842.16", "USD · 演示账本", "＄"],
      ["预算租约健康", "98.7%", "在线实例 8 / 8", "◷"]
    ],
    activities: [
      ["预算租约已续期", "Line Item · Summer Launch", "刚刚"],
      ["创意已发布", "Spring Collection · 728 × 90", "12 分钟前"],
      ["结算已幂等确认", "event · 8a4f…d109", "27 分钟前"]
    ],
    resources: {
      campaigns: {
        title: "Campaign",
        description: "管理广告主活动及其投放时间窗。",
        noun: "Campaign",
        columns: [["name", "Campaign"], ["advertiser", "Advertiser"], ["period", "投放周期"], ["lineItems", "Line Items"], ["status", "状态"]],
        fields: [
          ["name", "Campaign 名称", "text", "例如 Summer Launch"],
          ["advertiser", "Advertiser", "select", [["Acme Studio", "Acme Studio"], ["Mori Living", "Mori Living"]]],
          ["period", "投放周期", "text", "2026-10-01 至 2026-12-31"]
        ],
        rows: [
          { name: "Autumn Collection", id: "cmp_autumn_2026", advertiser: "Mori Living", period: "2026-09-01 — 2026-11-30", lineItems: "4", status: "active", objective: "Traffic", version: "v12" },
          { name: "Studio Opening", id: "cmp_studio_open", advertiser: "Acme Studio", period: "2026-10-01 — 2026-12-31", lineItems: "2", status: "active", objective: "Awareness", version: "v8" },
          { name: "Winter Preview", id: "cmp_winter_preview", advertiser: "Mori Living", period: "2026-11-01 — 2027-01-31", lineItems: "0", status: "draft", objective: "Traffic", version: "v1" }
        ]
      },
      "line-items": {
        title: "Line Item",
        description: "定义 bid、targeting、素材关联与 daily/lifetime 预算。",
        noun: "Line Item",
        columns: [["name", "Line Item"], ["campaign", "Campaign"], ["bid", "Bid CPM"], ["budget", "Daily / Lifetime"], ["delivery", "预算使用"], ["status", "状态"]],
        fields: [
          ["name", "Line Item 名称", "text", "例如 Tokyo Display"],
          ["campaign", "Campaign", "select", [["Autumn Collection", "Autumn Collection"], ["Studio Opening", "Studio Opening"]]],
          ["bid", "Bid CPM (micros)", "number", "2500000"],
          ["budget", "Daily budget (micros)", "number", "1000000000"],
          ["targeting", "Targeting 域名", "text", "例如 publisher.test"]
        ],
        rows: [
          { name: "Tokyo Display", id: "li_tokyo_display", campaign: "Autumn Collection", bid: "USD 2.50", budget: "$500 / $10K", delivery: "42%", status: "active", targeting: "sakura.example · 300×250", creatives: "3 个" },
          { name: "Brand Awareness", id: "li_brand_awareness", campaign: "Studio Opening", bid: "USD 1.80", budget: "$320 / $5K", delivery: "28%", status: "active", targeting: "northstar.test · 728×90", creatives: "2 个" },
          { name: "Retargeting Draft", id: "li_retarget_draft", campaign: "Winter Preview", bid: "USD 3.00", budget: "$0 / $2K", delivery: "0%", status: "draft", targeting: "segment targeting 不在首期范围", creatives: "1 个" }
        ]
      },
      creatives: {
        title: "Creative 素材",
        description: "登记和发布静态图片创意；文件由服务端校验格式、尺寸与 hash。",
        noun: "Creative",
        columns: [["name", "Creative"], ["format", "格式 / 尺寸"], ["asset", "Asset hash"], ["campaign", "关联活动"], ["status", "状态"]],
        fields: [
          ["name", "Creative 名称", "text", "例如 Autumn Hero"],
          ["format", "格式", "select", [["image", "静态图片"], ["banner", "Image banner"]]],
          ["size", "尺寸", "select", [["728x90", "728 × 90"], ["300x250", "300 × 250"], ["320x50", "320 × 50"]]],
          ["campaign", "Campaign", "select", [["Autumn Collection", "Autumn Collection"], ["Studio Opening", "Studio Opening"]]]
        ],
        rows: [
          { name: "Autumn Hero", id: "cr_autumn_hero", format: "PNG · 728 × 90", asset: "a82f…c104", campaign: "Autumn Collection", status: "published", landing: "mori.example/autumn", version: "v4" },
          { name: "Mori Living Card", id: "cr_mori_card", format: "WebP · 300 × 250", asset: "60a1…ef42", campaign: "Autumn Collection", status: "published", landing: "mori.example/living", version: "v2" },
          { name: "Studio Preview", id: "cr_studio_preview", format: "JPEG · 320 × 50", asset: "—", campaign: "Studio Opening", status: "draft", landing: "acme.example/studio", version: "v1" }
        ]
      },
      budgets: {
        title: "预算与账本",
        description: "查看 Line Item 预算 scope、spent、租约与可用余额；此原型只展示演示数据。",
        noun: "预算账户",
        columns: [["name", "Line Item"], ["scope", "预算 Scope"], ["limit", "预算上限"], ["spent", "已消耗"], ["leased", "已租额度"], ["available", "可用余额"]],
        fields: [],
        rows: [
          { name: "Tokyo Display", id: "li_tokyo_display", scope: "Daily · 2026-10-12", limit: "USD 500.00", spent: "USD 208.40", leased: "USD 22.00", available: "USD 269.60", status: "healthy", currency: "USD" },
          { name: "Tokyo Display", id: "li_tokyo_display", scope: "Lifetime", limit: "USD 10,000.00", spent: "USD 1,842.16", leased: "USD 64.00", available: "USD 8,093.84", status: "healthy", currency: "USD" },
          { name: "Brand Awareness", id: "li_brand_awareness", scope: "Daily · 2026-10-12", limit: "USD 320.00", spent: "USD 89.60", leased: "USD 16.00", available: "USD 214.40", status: "healthy", currency: "USD" }
        ]
      },
      quarantine: {
        title: "账务隔离",
        description: "检查迟到、冲突或校验失败的 billable notice；处置操作仅为原型模拟。",
        noun: "隔离记录",
        columns: [["name", "Reservation"], ["reason", "原因"], ["amount", "金额"], ["received", "接收时间"], ["status", "处理状态"]],
        fields: [],
        rows: [
          { name: "rsv_8a4f…d109", id: "quarantine_1042", reason: "notice 超过 grace window", amount: "USD 0.0042", received: "11 分钟前", status: "pending", auction: "auc_54c…091b", hash: "sha256:9f8a…1e0c" },
          { name: "rsv_c902…4b10", id: "quarantine_1041", reason: "相同 event ID 内容冲突", amount: "USD 0.0038", received: "昨天", status: "pending", auction: "auc_04a…77d2", hash: "sha256:11c2…a118" }
        ]
      }
    }
  },
  dmp: {
    name: "DMP 受众平台",
    shortName: "DMP",
    initials: "M",
    description: "管理数据源、受众定义、不可变快照与隐私撤回状态。",
    nav: [
      ["dashboard", "数据总览", "⌂"],
      ["providers", "Data Providers", "◉"],
      ["segments", "Segments", "◫"],
      ["snapshots", "Membership Snapshots", "▤"],
      ["revocations", "撤回与隐私门禁", "⊘"]
    ],
    stats: [
      ["已发布快照", "12", "只读已校验版本", "▤"],
      ["可用 Segments", "48", "首期只透传至 DSP", "◫"],
      ["今日解析请求", "186K", "deny gate 正常", "⌁"],
      ["待处理撤回", "2", "持续 drain / 删除", "◷"]
    ],
    activities: [
      ["快照已发布", "snapshot · snap_20261012_01", "8 分钟前"],
      ["撤回已完成在线屏障", "revocation · rvk_2091", "21 分钟前"],
      ["Provider 数据已导入", "Sakura Audience · 42,180 rows", "1 小时前"]
    ],
    resources: {
      providers: {
        title: "Data Providers",
        description: "登记数据提供方、identity namespace 和发布状态。",
        noun: "Provider",
        columns: [["name", "Provider"], ["namespace", "Namespace"], ["segments", "Segments"], ["lastSnapshot", "最近快照"], ["status", "状态"]],
        fields: [
          ["name", "Provider 名称", "text", "例如 Sakura Audience"],
          ["namespace", "Identity namespace", "text", "例如 sakura-member-v1"],
          ["contact", "数据联系人", "email", "data@example.jp"],
          ["retention", "快照保留", "select", [["90d", "最长 3 个月"], ["custom", "按策略配置"]]]
        ],
        rows: [
          { name: "Sakura Audience", id: "provider_sakura", namespace: "sakura-member-v1", segments: "12", lastSnapshot: "今天 09:12", status: "active", keyVersions: "v3 · v4", refresh: "每日" },
          { name: "Northstar Context", id: "provider_northstar", namespace: "northstar-context-v2", segments: "8", lastSnapshot: "今天 08:46", status: "active", keyVersions: "v2", refresh: "每日" },
          { name: "Cedar Retail", id: "provider_cedar", namespace: "cedar-retail-v1", segments: "—", lastSnapshot: "—", status: "inactive", keyVersions: "—", refresh: "—" }
        ]
      },
      segments: {
        title: "Segments",
        description: "维护受众定义和版本。首期 segment 只透传，不参与 DSP targeting。",
        noun: "Segment",
        columns: [["name", "Segment"], ["provider", "Provider"], ["members", "成员数"], ["updated", "更新时间"], ["status", "状态"]],
        fields: [
          ["name", "Segment 名称", "text", "例如 Home & Living"],
          ["provider", "Provider", "select", [["Sakura Audience", "Sakura Audience"], ["Northstar Context", "Northstar Context"]]],
          ["description", "描述", "text", "受众定义说明"]
        ],
        rows: [
          { name: "Home & Living", id: "seg_home_living", provider: "Sakura Audience", members: "42,180", updated: "今天 09:12", status: "published", version: "v8", purpose: "contextual audience" },
          { name: "Frequent Readers", id: "seg_readers", provider: "Northstar Context", members: "18,920", updated: "今天 08:46", status: "published", version: "v5", purpose: "contextual audience" },
          { name: "Travel Intent", id: "seg_travel_intent", provider: "Sakura Audience", members: "—", updated: "—", status: "draft", version: "v1", purpose: "draft" }
        ]
      },
      snapshots: {
        title: "Membership Snapshots",
        description: "导入、校验并发布不可变 membership snapshot；未发布数据不会进入在线解析。",
        noun: "Snapshot",
        columns: [["name", "Snapshot"], ["provider", "Provider"], ["rows", "记录数"], ["checksum", "Checksum"], ["status", "状态"]],
        fields: [
          ["name", "Snapshot 标识", "text", "例如 snap_20261012_02"],
          ["provider", "Provider", "select", [["Sakura Audience", "Sakura Audience"], ["Northstar Context", "Northstar Context"]]],
          ["rows", "预期记录数", "number", "42000"],
          ["checksum", "Source checksum", "text", "sha256:…"]
        ],
        rows: [
          { name: "snap_20261012_01", id: "snapshot_20261012_01", provider: "Sakura Audience", rows: "42,180", checksum: "a82f…c104", status: "published", created: "今天 09:12", expires: "2027-01-12" },
          { name: "snap_20261011_01", id: "snapshot_20261011_01", provider: "Northstar Context", rows: "18,920", checksum: "60a1…ef42", status: "published", created: "昨天 08:46", expires: "2027-01-11" },
          { name: "snap_20261012_02", id: "snapshot_20261012_02", provider: "Sakura Audience", rows: "42,180", checksum: "校验中", status: "processing", created: "今天 13:34", expires: "未发布" }
        ]
      },
      revocations: {
        title: "撤回与隐私门禁",
        description: "仅展示撤回任务状态、deny gate 与物理删除进度；不暴露 subject token。",
        noun: "撤回任务",
        columns: [["name", "Revocation ID"], ["namespace", "Namespace"], ["gate", "在线门禁"], ["drain", "Drain 状态"], ["deletion", "物理删除"], ["status", "任务状态"]],
        fields: [
          ["namespace", "Identity namespace", "select", [["sakura-member-v1", "sakura-member-v1"], ["northstar-context-v2", "northstar-context-v2"]]],
          ["reason", "撤回来源", "select", [["subject", "Subject 请求"], ["publisher", "Publisher 管理请求"]]],
          ["requestId", "外部请求 ID（可选）", "text", "仅用于关联，不输入 token"]
        ],
        rows: [
          { name: "rvk_2091", id: "revocation_2091", namespace: "sakura-member-v1", gate: "已关闭", drain: "完成 · 0 in-flight", deletion: "截止 11/11", status: "complete", created: "21 分钟前", token: "已脱敏" },
          { name: "rvk_2092", id: "revocation_2092", namespace: "sakura-member-v1", gate: "已关闭", drain: "等待 · 2 in-flight", deletion: "等待排程", status: "pending", created: "6 分钟前", token: "已脱敏" },
          { name: "rvk_2087", id: "revocation_2087", namespace: "northstar-context-v2", gate: "已关闭", drain: "完成 · 0 in-flight", deletion: "已完成", status: "complete", created: "昨天", token: "已脱敏" }
        ]
      }
    }
  }
};

if (!serviceConfigs[serviceKey]) {
  location.replace("./index.html");
} else {
  const config = serviceConfigs[serviceKey];
  const resourceKeys = Object.keys(config.resources);
  for (const resource of Object.values(config.resources)) resource.rows = structuredClone(resource.rows);
  let currentPage = location.hash.slice(1) || "dashboard";
  let searchText = "";
  let filterStatus = "all";
  let toastTimeout;

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function serviceLink(key, label) {
    const anchor = el("a", "", label);
    anchor.href = `./${key}.html`;
    if (key === serviceKey) anchor.setAttribute("aria-current", "page");
    return anchor;
  }

  function renderShell() {
    app.replaceChildren();
    const shell = el("div", "shell");
    const sidebar = el("aside", "sidebar");
    sidebar.setAttribute("aria-label", "服务导航");

    const brand = el("a", "console-brand");
    brand.href = "./index.html";
    brand.setAttribute("aria-label", "Open Ads 原型首页");
    brand.innerHTML = '<span class="brand-mark" aria-hidden="true"><span></span><span></span><span></span></span><span>openads</span>';
    sidebar.append(brand);

    const serviceSelect = el("div", "service-select");
    const glyph = el("span", "service-glyph", config.initials);
    const serviceName = el("span");
    serviceName.append(el("strong", "", config.name), el("small", "", "Sandbox workspace"));
    serviceSelect.append(glyph, serviceName, el("span", "chevron", "⌄"));
    sidebar.append(serviceSelect);
    sidebar.append(el("div", "sidebar-label", "WORKSPACE"));

    const nav = el("nav", "nav-list");
    nav.setAttribute("aria-label", `${config.shortName} 工作区`);
    for (const [key, label, icon] of config.nav) {
      const anchor = el("a", "nav-link");
      anchor.href = `#${key}`;
      if (currentPage === key) anchor.setAttribute("aria-current", "page");
      anchor.append(el("span", "nav-icon", icon), el("span", "", label));
      nav.append(anchor);
    }
    sidebar.append(nav, el("div", "sidebar-spacer"), el("div", "sidebar-separator"));
    const serviceLabel = el("div", "sidebar-label", "SERVICE PROTOTYPES");
    const switcher = el("div", "service-switch");
    switcher.append(serviceLink("ssp", "SSP"), serviceLink("dsp", "DSP"), serviceLink("dmp", "DMP"));
    sidebar.append(serviceLabel, switcher, el("div", "sidebar-separator"));
    const meta = el("div", "side-meta");
    const modeMeta = el("div");
    modeMeta.append(document.createTextNode("运行模式 · "), el("b", "", "Sandbox"));
    meta.append(modeMeta);
    meta.append(el("div", "", "演示数据 · 不连接生产"));
    sidebar.append(meta);

    const mainShell = el("div", "main-shell");
    const topbar = el("header", "topbar");
    const crumbs = el("div", "breadcrumbs");
    crumbs.append(el("span", "", "Open Ads"), el("span", "", "/"), el("strong", "", config.shortName));
    const topActions = el("div", "topbar-actions");
    const env = el("span", "env-chip");
    env.append(el("i"), document.createTextNode("测试环境"));
    const user = el("div", "user-chip");
    user.append(el("span", "user-avatar", "S"), el("span", "", "Sandbox Operator"));
    topActions.append(env, user);
    topbar.append(crumbs, topActions);
    const main = el("main");
    main.id = "main-content";
    main.tabIndex = -1;
    const toast = el("div", "toast");
    toast.id = "console-toast";
    toast.setAttribute("role", "status");
    toast.setAttribute("aria-live", "polite");
    toast.hidden = true;
    mainShell.append(topbar, main);
    shell.append(sidebar, mainShell, toast);
    app.append(shell);
    renderPage();
  }

  function pageTitle(page) {
    if (page === "dashboard") return ["服务总览", config.description, "Dashboard"];
    const resource = config.resources[page];
    if (!resource) return ["页面未找到", "选择左侧导航查看服务资源。", "Not found"];
    return [resource.title, resource.description, "管理"];
  }

  function renderPage() {
    const main = document.querySelector("#main-content");
    main.replaceChildren();
    const [title, description, eyebrow] = pageTitle(currentPage);
    document.title = `${title} · ${config.shortName} · Open Ads`;
    const pageHeader = el("section", "page-header");
    const titleGroup = el("div");
    const heading = el("h1", "", title);
    heading.tabIndex = -1;
    titleGroup.append(el("div", "eyebrow", `${config.shortName} / ${eyebrow}`), heading, el("p", "page-description", description));
    pageHeader.append(titleGroup);
    if (currentPage === "dashboard") {
      const actions = el("div", "header-actions");
      const status = el("span", "env-chip");
      status.append(el("i"), document.createTextNode("所有系统正常"));
      actions.append(status, makeButton("查看 API 文档", "button", () => showToast("此原型使用模拟数据，API 尚未连接。")));
      pageHeader.append(actions);
      main.append(pageHeader, renderDashboard());
    } else {
      const resource = config.resources[currentPage];
      const actions = el("div", "header-actions");
      if (currentPage === "snapshots") {
        actions.append(makeButton("＋ 导入 Snapshot", "button button-primary", () => openCreateDialog(resource)));
      } else if (currentPage === "revocations") {
        actions.append(makeButton("⊘ 模拟撤回", "button button-danger", () => openCreateDialog(resource)));
      } else if (resource.fields.length) {
        actions.append(makeButton(`＋ 新建 ${resource.noun}`, "button button-primary", () => openCreateDialog(resource)));
      } else {
        actions.append(makeButton("刷新数据", "button", () => showToast("已刷新演示数据")));
      }
      pageHeader.append(actions);
      main.append(pageHeader);
      main.append(renderResourcePage(resource));
    }
    main.append(renderFooter());
  }

  function makeButton(label, className, action, ariaLabel) {
    const button = el("button", className, label);
    button.type = "button";
    if (ariaLabel) button.setAttribute("aria-label", ariaLabel);
    button.addEventListener("click", action);
    return button;
  }

  function renderDashboard() {
    const fragment = document.createDocumentFragment();
    const note = el("div", "callout");
    note.append(el("span", "", "ⓘ"), el("div"));
    note.lastChild.append(el("strong", "", "原型环境："), document.createTextNode("当前页面使用演示数据，创建、发布和处置操作只在本地模拟，不会调用服务端或产生真实投放/结算。"));
    fragment.append(note);

    const stats = el("section", "stats-grid");
    stats.setAttribute("aria-label", `${config.shortName} 运营指标`);
    for (const [label, value, foot, icon] of config.stats) {
      const card = el("article", "stat-card");
      const top = el("div", "stat-top");
      top.append(el("span", "", label), el("span", "stat-symbol", icon));
      card.append(top, el("div", "stat-value", value), el("div", "stat-foot", foot));
      stats.append(card);
    }
    fragment.append(stats);

    const content = el("section", "content-grid");
    content.append(renderRecentResource(), renderActivities());
    fragment.append(content);
    const lower = el("section", "bottom-grid");
    lower.append(renderHealthCard(), renderReleaseCard());
    fragment.append(lower);
    return fragment;
  }

  function renderRecentResource() {
    const resourceKey = resourceKeys[0];
    const resource = config.resources[resourceKey];
    const card = el("section", "card");
    card.append(cardHeader(`最近${resource.noun}`, "查看全部", () => navigate(resourceKey), `${resource.title} 最近更新`));
    const table = el("table");
    table.append(makeTableHead(resource.columns.slice(0, 3)));
    const body = el("tbody");
    for (const row of resource.rows.slice(0, 4)) body.append(makeTableRow(resource, row, resource.columns.slice(0, 3), false));
    table.append(body);
    const wrap = el("div", "table-wrap");
    wrap.append(table);
    card.append(wrap);
    return card;
  }

  function renderActivities() {
    const card = el("section", "card activity-card");
    card.append(cardHeader("最近活动", "查看状态", () => navigate(resourceKeys.at(-1)), "最近活动记录"));
    const list = el("div", "activity-list");
    for (const [title, detail, time] of config.activities) {
      const item = el("div", "activity-item");
      item.append(el("span", "activity-mark"), el("div", "activity-copy"));
      item.lastChild.append(el("strong", "", title), document.createTextNode(` · ${detail}`), el("time", "", time));
      list.append(item);
    }
    card.append(list);
    return card;
  }

  function renderHealthCard() {
    const card = el("section", "card");
    card.append(cardHeader("服务健康", "Sandbox", () => showToast("健康检查仅为原型状态")));
    const metrics = serviceKey === "dmp"
      ? [["Audience Resolve P95", "24 ms"], ["Snapshot 最新版本", "snap_20261012_01"], ["Deny gate", "Healthy"]]
      : serviceKey === "dsp"
        ? [["Bid endpoint P95", "42 ms"], ["Ready 实例", "8 / 8"], ["预算租约", "Healthy"]]
        : [["Auction P95", "67 ms"], ["Ready 实例", "6 / 6"], ["配置 manifest", "892"]];
    const list = el("div");
    for (const [label, value] of metrics) {
      const row = el("div", "mini-metric");
      row.append(el("span", "", label), el("strong", "", value));
      list.append(row);
    }
    card.append(list);
    return card;
  }

  function renderReleaseCard() {
    const card = el("section", "card");
    card.append(cardHeader("配置与数据发布", "当前状态", () => navigate(resourceKeys.at(-1))));
    const body = el("div", "", "");
    body.style.padding = "13px";
    const release = el("div", "release-card");
    const title = el("div", "release-title");
    title.append(el("span", "", serviceKey === "dmp" ? "快照校验 / 发布" : "热配置实例加载"), el("b", "", serviceKey === "dmp" ? "12 / 12" : "6 / 6"));
    const bar = el("div", "release-bar");
    const fill = el("span");
    fill.style.width = serviceKey === "dmp" ? "86%" : "100%";
    bar.append(fill);
    const foot = el("div", "release-foot");
    foot.append(el("span", "", serviceKey === "dmp" ? "1 个 snapshot 校验中" : "所有实例已同步"), el("span", "", serviceKey === "dmp" ? "86%" : "100%"));
    release.append(title, bar, foot);
    body.append(release);
    card.append(body);
    return card;
  }

  function cardHeader(title, linkText, linkAction, ariaLabel = title) {
    const header = el("div", "card-header");
    const titleNode = el("div");
    titleNode.append(el("h2", "", title));
    const link = el("button", "card-link", linkText);
    link.type = "button";
    link.setAttribute("aria-label", ariaLabel);
    link.addEventListener("click", linkAction);
    header.append(titleNode, link);
    return header;
  }

  function makeTableHead(columns) {
    const thead = el("thead");
    const row = el("tr");
    for (const [, label] of columns) row.append(el("th", "", label));
    thead.append(row);
    return thead;
  }

  function displayValue(resourceKey, row, key) {
    if (key === "status") return localizeStatus(row.status);
    return row[key] || "—";
  }

  function localizeStatus(status) {
    const labels = { active: "运行中", published: "已发布", healthy: "健康", complete: "已完成", draft: "草稿", pending: "待处理", processing: "处理中", paused: "已暂停", inactive: "已停用", failed: "失败", blocked: "已阻止" };
    return labels[status] || status || "—";
  }

  function makeTableRow(resource, row, columns, withActions = true) {
    const tr = el("tr");
    for (const [key] of columns) {
      const td = el("td");
      if (key === "name") {
        const main = el("div", "cell-main", row.name);
        const sub = el("div", "cell-sub", row.id);
        sub.setAttribute("translate", "no");
        td.append(main, sub);
      } else if (key === "status") {
        const pill = el("span", `status-pill ${row.status}`, localizeStatus(row.status));
        td.append(pill);
      } else {
        td.textContent = displayValue(currentPage, row, key);
      }
      tr.append(td);
    }
    if (withActions) {
      const actionCell = el("td");
      const actions = el("div", "row-actions");
      actions.append(makeButton("⋯", "icon-button", () => openDetails(resource, row), `查看 ${row.name || row.id} 详情`));
      if (row.status === "draft" || row.status === "pending" && currentPage === "config") {
        actions.append(makeButton("↑", "icon-button", () => runPrimaryAction(resource, row), `发布或重试 ${row.name || row.id}`));
      } else if (row.status === "processing" && currentPage === "snapshots") {
        actions.append(makeButton("✓", "icon-button", () => runPrimaryAction(resource, row), `校验 ${row.name || row.id}`));
      } else if (row.status === "pending" && currentPage === "revocations") {
        actions.append(makeButton("✓", "icon-button", () => runPrimaryAction(resource, row), `模拟完成 drain ${row.name || row.id}`));
      } else if (currentPage === "quarantine" && row.status === "pending") {
        actions.append(makeButton("✓", "icon-button", () => resolveQuarantine(row), `模拟处置 ${row.id}`));
      }
      actionCell.append(actions);
      tr.append(actionCell);
    }
    return tr;
  }

  function renderResourcePage(resource) {
    const section = el("section");
    if (currentPage === "revocations") section.append(makePrivacyNote());
    if (currentPage === "line-items") section.append(makeMvpNote());
    if (currentPage === "snapshots") section.append(makeSnapshotNote());

    const toolbar = el("div", "toolbar");
    const left = el("div", "toolbar-left");
    const search = el("label", "search-box");
    search.append(el("span", "", "⌕"));
    const input = el("input");
    input.type = "search";
    input.name = "resource-search";
    input.autocomplete = "off";
    input.placeholder = `搜索${resource.noun}…`;
    input.setAttribute("aria-label", `搜索${resource.noun}`);
    input.value = searchText;
    input.addEventListener("input", () => {
      searchText = input.value.trim().toLowerCase();
      renderTableRows(resource, tableBody);
    });
    search.append(input);
    left.append(search);
    const right = el("div", "toolbar-right");
    const count = el("span", "eyebrow", `${resource.rows.length} 条记录`);
    right.append(count);
    if (currentPage !== "config" && currentPage !== "budgets" && currentPage !== "quarantine" && currentPage !== "revocations") {
      const filter = el("select", "filter-select");
      filter.name = "status-filter";
      filter.setAttribute("aria-label", "按状态筛选");
      const statusLabels = { active: "运行中", published: "已发布", healthy: "健康", draft: "草稿", pending: "待处理", processing: "处理中", paused: "已暂停", inactive: "已停用", complete: "已完成" };
      const statuses = [...new Set(resource.rows.map((row) => row.status).filter(Boolean))];
      const options = [["all", "全部状态"], ...statuses.map((status) => [status, statusLabels[status] || status])];
      for (const [value, label] of options) {
        const option = el("option", "", label);
        option.value = value;
        filter.append(option);
      }
      filter.value = filterStatus;
      filter.addEventListener("change", () => {
        filterStatus = filter.value;
        renderTableRows(resource, tableBody);
      });
      right.append(filter);
    }
    toolbar.append(left, right);
    section.append(toolbar);

    const card = el("section", "card table-card");
    const wrap = el("div", "table-wrap");
    const table = el("table");
    const columns = [...resource.columns, ["actions", "操作"]];
    table.append(makeTableHead(columns));
    const tableBody = el("tbody");
    renderTableRows(resource, tableBody);
    table.append(tableBody);
    wrap.append(table);
    card.append(wrap);
    const footer = el("div", "table-footer");
    footer.append(el("span", "", `演示数据 · ${resource.rows.length} 条记录`));
    const pagination = el("div", "pagination");
    pagination.append(el("span", "page-number current", "1"), el("span", "page-number", "›"));
    footer.append(pagination);
    card.append(footer);
    section.append(card);
    return section;
  }

  function renderTableRows(resource, body) {
    const filtered = resource.rows.filter((row) => {
      const matchesSearch = !searchText || Object.values(row).some((value) => String(value).toLowerCase().includes(searchText));
      const matchesStatus = filterStatus === "all" || row.status === filterStatus;
      return matchesSearch && matchesStatus;
    });
    body.replaceChildren();
    if (!filtered.length) {
      const tr = el("tr");
      const td = el("td", "empty-table");
      td.colSpan = resource.columns.length + 1;
      td.append(el("strong", "", "没有找到匹配记录"), document.createTextNode("调整搜索词或筛选条件后重试。"));
      tr.append(td);
      body.append(tr);
      return;
    }
    for (const row of filtered) body.append(makeTableRow(resource, row, resource.columns));
  }

  function makePrivacyNote() {
    const note = el("div", "callout");
    note.append(el("span", "", "◈"), el("div"));
    note.lastChild.append(el("strong", "", "隐私安全："), document.createTextNode("该页面只显示撤回任务状态，不会读取或展示 subject token、原始标识或 membership 内容。在线 deny gate 关闭与物理删除是不同阶段。"));
    return note;
  }

  function makeMvpNote() {
    const note = el("div", "callout");
    note.append(el("span", "", "ⓘ"), el("div"));
    note.lastChild.append(el("strong", "", "首期边界："), document.createTextNode("只支持 site.domain、site.page 路径前缀、banner 尺寸和预算；频控、pacing 与 segment targeting 暂不支持。"));
    return note;
  }

  function makeSnapshotNote() {
    const note = el("div", "callout");
    note.append(el("span", "", "↻"), el("div"));
    note.lastChild.append(el("strong", "", "发布流程："), document.createTextNode("导入 → 校验行数与 checksum → 发布不可变 snapshot。校验中的 snapshot 不会被在线解析使用。"));
    return note;
  }

  function renderFooter() {
    const footer = el("footer", "page-footer");
    footer.append(el("span", "", `Open Ads ${config.shortName} Console`), el("span", "", "原型演示 · 不连接生产服务"));
    return footer;
  }

  function navigate(page) {
    if (location.hash === `#${page}`) {
      currentPage = page;
      searchText = "";
      filterStatus = "all";
      renderPage();
      return;
    }
    location.hash = page;
  }

  function showToast(message) {
    const toast = document.querySelector("#console-toast");
    toast.textContent = message;
    toast.hidden = false;
    window.clearTimeout(toastTimeout);
    toastTimeout = window.setTimeout(() => { toast.hidden = true; }, 2300);
  }

  function openDialog(title, description, content) {
    document.querySelectorAll("dialog:not([open])").forEach((closedDialog) => closedDialog.remove());
    const dialog = document.createElement("dialog");
    dialog.setAttribute("aria-labelledby", "modal-title");
    const head = el("div", "dialog-head");
    const heading = el("div");
    const titleNode = el("h2", "", title);
    titleNode.id = "modal-title";
    heading.append(titleNode, el("p", "", description));
    const close = makeButton("×", "dialog-close", () => dialog.close(), "关闭对话框");
    head.append(heading, close);
    dialog.append(head, content);
    dialog.addEventListener("close", () => dialog.remove(), { once: true });
    dialog.addEventListener("click", (event) => {
      if (event.target === dialog) dialog.close();
    });
    document.body.append(dialog);
    dialog.showModal();
    close.focus();
    return dialog;
  }

  function openCreateDialog(resource) {
    if (currentPage === "snapshots") return openSnapshotImport(resource);
    if (currentPage === "revocations") return openRevocationDialog(resource);
    const form = el("form", "dialog-form");
    form.noValidate = true;
    const grid = el("div", "form-grid");
    const controls = new Map();
    for (const [name, label, type, configValue] of resource.fields) {
      const field = el("div", "form-field");
      const labelNode = el("label", "", label);
      labelNode.htmlFor = `field-${name}`;
      const control = makeFieldControl(name, type, configValue);
      control.id = `field-${name}`;
      control.name = name;
      control.required = name !== "contact" && type !== "email";
      field.append(labelNode, control);
      controls.set(name, control);
      grid.append(field);
    }
    const error = el("p", "form-error");
    error.setAttribute("role", "alert");
    error.hidden = true;
    form.append(grid, error);
    const actions = el("div", "dialog-actions");
    actions.append(makeButton("取消", "button", () => form.closest("dialog").close()));
    const submit = el("button", "button button-primary", "创建演示记录");
    submit.type = "submit";
    actions.append(submit);
    form.append(error, actions);
    const dialog = openDialog(`新建${resource.noun}`, "仅添加到当前页面的本地演示数据。", form);
    form.addEventListener("input", () => { error.hidden = true; });
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      const invalid = [...controls.values()].find((control) => control.required && !control.value.trim() || !control.checkValidity());
      if (invalid) {
        error.textContent = "请填写所有必填项，并检查字段格式。";
        error.hidden = false;
        invalid.focus();
        return;
      }
      const values = Object.fromEntries([...controls].map(([key, control]) => [key, control.value.trim()]));
      const row = buildResourceRow(currentPage, values);
      resource.rows.unshift(row);
      dialog.close();
      searchText = "";
      filterStatus = "all";
      renderPage();
      showToast(`${resource.noun}已添加到本地演示数据`);
    });
  }

  function makeFieldControl(name, type, configValue) {
    if (type === "select") {
      const select = el("select");
      for (const [value, label] of configValue) {
        const option = el("option", "", label);
        option.value = value;
        select.append(option);
      }
      return select;
    }
    const input = el("input");
    input.type = type === "url" ? "url" : type === "email" ? "email" : type === "number" ? "number" : "text";
    input.placeholder = configValue || "";
    if (type === "number") input.min = "0";
    if (type === "text" || type === "email" || type === "url") input.autocomplete = "off";
    return input;
  }

  function buildResourceRow(page, values) {
    const name = values.name || values.namespace || values.requestId || "新建记录";
    const base = { name, id: `${page}_${Date.now().toString(36)}`, status: "draft", version: "v1" };
    if (serviceKey === "ssp" && page === "publishers") return { ...base, property: values.property || "待配置", requests: "—", owner: "Sandbox Tenant" };
    if (serviceKey === "ssp" && page === "placements") return { ...base, publisher: values.publisher, size: values.size?.replace("x", " × "), floor: `USD ${values.floor || "0.00"}`, policy: "First-price · at=1" };
    if (serviceKey === "ssp" && page === "supply") return { ...base, publisher: values.publisher, protocol: values.protocol || "OpenRTB 2.6", lastSeen: "尚无请求", auth: "mTLS" };
    if (serviceKey === "ssp" && page === "dsp-endpoints") return { ...base, timeout: `${values.timeout || "80"} ms`, priority: values.priority || "10", p95: "—", callback: "待配置 allowlist" };
    if (serviceKey === "dsp" && page === "campaigns") return { ...base, advertiser: values.advertiser, period: values.period || "待设置", lineItems: "0", objective: "Traffic" };
    if (serviceKey === "dsp" && page === "line-items") return { ...base, campaign: values.campaign, bid: `USD ${(Number(values.bid || 0) / 1000000).toFixed(2)}`, budget: `USD ${(Number(values.budget || 0) / 1000000).toFixed(2)} / lifetime`, delivery: "0%", targeting: values.targeting || "不限", creatives: "0 个" };
    if (serviceKey === "dsp" && page === "creatives") return { ...base, format: "待上传 · " + (values.size || "image"), asset: "未上传", campaign: values.campaign, landing: "待配置" };
    if (serviceKey === "dmp" && page === "providers") return { ...base, namespace: values.namespace, segments: "0", lastSnapshot: "—", keyVersions: "待配置", refresh: "待配置" };
    if (serviceKey === "dmp" && page === "segments") return { ...base, provider: values.provider, members: "—", updated: "刚刚", purpose: values.description || "待审核" };
    return { ...base, ...values };
  }

  function openSnapshotImport(resource) {
    const form = el("form", "dialog-form");
    form.noValidate = true;
    const grid = el("div", "form-grid");
    const controls = new Map();
    for (const [name, label, type, configValue] of resource.fields) {
      const field = el("div", "form-field");
      const labelNode = el("label", "", label);
      labelNode.htmlFor = `snapshot-${name}`;
      const control = makeFieldControl(name, type, configValue);
      control.id = `snapshot-${name}`;
      control.name = name;
      control.required = true;
      field.append(labelNode, control);
      controls.set(name, control);
      grid.append(field);
    }
    const hint = el("div", "form-field full");
    hint.append(el("small", "", "此原型不上传成员数据，也不接受或显示 subject token；仅模拟创建 snapshot 导入任务。"));
    grid.append(hint);
    const error = el("p", "form-error");
    error.setAttribute("role", "alert");
    error.hidden = true;
    form.append(grid, error);
    const actions = el("div", "dialog-actions");
    actions.append(makeButton("取消", "button", () => form.closest("dialog").close()));
    const submit = el("button", "button button-primary", "创建导入任务");
    submit.type = "submit";
    actions.append(submit);
    form.append(actions);
    const dialog = openDialog("导入 Membership Snapshot", "添加一个本地模拟的快照导入任务。", form);
    form.addEventListener("input", () => { error.hidden = true; });
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      const invalid = [...controls.values()].find((control) => !control.value.trim() || !control.checkValidity());
      if (invalid) {
        error.textContent = "请填写 Snapshot 标识、Provider、记录数和有效 checksum。";
        error.hidden = false;
        invalid.focus();
        return;
      }
      const values = Object.fromEntries([...controls].map(([key, control]) => [key, control.value.trim()]));
      const name = values.name || `snap_${new Date().toISOString().slice(0, 10).replaceAll("-", "")}_demo`;
      resource.rows.unshift({ name, id: `snapshot_${Date.now().toString(36)}`, provider: values.provider, rows: values.rows ? Number(values.rows).toLocaleString() : "待校验", checksum: values.checksum || "待校验", status: "processing", created: "刚刚", expires: "未发布" });
      dialog.close();
      renderPage();
      showToast("已创建模拟导入任务；在线查询仍使用上一已发布快照");
    });
  }

  function openRevocationDialog(resource) {
    const form = el("form", "dialog-form");
    const grid = el("div", "form-grid");
    const controls = new Map();
    for (const [name, label, type, configValue] of resource.fields) {
      const field = el("div", "form-field");
      const labelNode = el("label", "", label);
      labelNode.htmlFor = `revocation-${name}`;
      const control = makeFieldControl(name, type, configValue);
      control.id = `revocation-${name}`;
      control.name = name;
      field.append(labelNode, control);
      controls.set(name, control);
      grid.append(field);
    }
    const hint = el("div", "form-field full");
    hint.append(el("small", "", "不会要求或接受 subject ID、HMAC token、email、电话或设备标识。确认后只新增脱敏的流程演示记录。"));
    grid.append(hint);
    form.append(grid);
    const actions = el("div", "dialog-actions");
    actions.append(makeButton("取消", "button", () => form.closest("dialog").close()));
    const submit = el("button", "button button-danger", "模拟关闭门禁");
    submit.type = "submit";
    actions.append(submit);
    form.append(actions);
    const dialog = openDialog("发起撤回演示", "展示 deny gate、in-flight drain 与物理删除的分阶段状态。", form);
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      const values = Object.fromEntries([...controls].map(([key, control]) => [key, control.value.trim()]));
      resource.rows.unshift({
        name: `rvk_demo_${Date.now().toString(36)}`,
        id: `revocation_${Date.now().toString(36)}`,
        namespace: values.namespace,
        gate: "已关闭",
        drain: "等待 · 1 in-flight",
        deletion: "等待排程",
        status: "pending",
        created: "刚刚",
        token: "未收集"
      });
      dialog.close();
      renderPage();
      showToast("演示撤回已创建：在线 admission 已阻止，等待 in-flight drain");
    });
  }

  function openDetails(resource, row) {
    const content = el("div", "detail-panel");
    const list = el("dl");
    const detailLabels = {
      id: "资源 ID", status: "状态", version: "配置版本", owner: "Tenant", property: "媒体资源", requests: "日请求量",
      policy: "拍卖策略", publisher: "Publisher", floor: "Floor CPM", size: "广告尺寸", protocol: "协议", auth: "认证方式",
      lastSeen: "最近请求", timeout: "DSP 超时", priority: "DSP 优先级", callback: "回调 Host 白名单", p95: "响应 P95",
      advertiser: "Advertiser", period: "投放周期", objective: "投放目标", campaign: "Campaign", bid: "出价",
      budget: "预算", delivery: "预算使用率", targeting: "Targeting", creatives: "关联素材", format: "素材格式",
      asset: "Asset SHA-256", landing: "落地页", scope: "预算 Scope", limit: "预算上限", spent: "已消耗",
      leased: "已租额度", available: "可用余额", currency: "币种", reason: "隔离原因", amount: "金额",
      received: "接收时间", auction: "Auction ID", hash: "Payload Hash", namespace: "Identity Namespace",
      segments: "Segment 数量", keyVersions: "Token key versions", refresh: "数据刷新频率", members: "成员数",
      updated: "更新时间", purpose: "定义/用途", rows: "记录数", checksum: "Checksum", created: "创建时间",
      expires: "数据到期", gate: "Deny gate", drain: "In-flight drain", deletion: "物理删除", token: "Subject token"
    };
    for (const [key, value] of Object.entries(row)) {
      const item = el("div", "detail-row");
      const label = el("dt", "", detailLabels[key] || key);
      const dd = el("dd", "", key === "status" ? localizeStatus(value) : String(value));
      if (key === "id" || key === "hash" || key === "auction") dd.setAttribute("translate", "no");
      item.append(label, dd);
      list.append(item);
    }
    content.append(list);
    const actions = el("div", "dialog-actions");
    actions.append(makeButton("关闭", "button", () => content.closest("dialog").close()));
    content.append(actions);
    openDialog(`${resource.noun}详情`, "只读演示数据；实际权限和字段由服务端授权决定。", content);
  }

  function runPrimaryAction(resource, row) {
    const isConfigRetry = currentPage === "config";
    const isSnapshotValidation = currentPage === "snapshots" && row.status === "processing";
    const isRevocationDrain = currentPage === "revocations" && row.status === "pending";
    const action = isConfigRetry ? "重试配置发布" : isSnapshotValidation ? "校验 Snapshot" : isRevocationDrain ? "完成撤回 Drain" : "发布";
    const content = el("div", "detail-panel");
    const note = el("div", "callout");
    note.append(el("span", "", "ⓘ"), el("div", "", isConfigRetry
      ? "此操作将在原型中把失败/待处理的 outbox 事件标记为已重试，不会调用真实服务。"
      : isSnapshotValidation
        ? "模拟检查导入记录数和 checksum。校验通过后状态变为草稿，仍需显式发布后才对在线读取可见。"
        : isRevocationDrain
          ? "模拟已登记的 in-flight lookup 排空。在线 deny gate 保持关闭；后续物理删除仍显示为独立阶段。"
      : "发布只修改本地演示状态；生产实现需要完成版本、关联资源与业务规则校验后才能进入竞价热路径。"));
    const actions = el("div", "dialog-actions");
    actions.append(makeButton("取消", "button", () => content.closest("dialog").close()));
    const confirmLabel = isConfigRetry ? "模拟重试" : isSnapshotValidation ? "确认校验" : isRevocationDrain ? "模拟完成 Drain" : "确认发布";
    const confirm = makeButton(confirmLabel, "button button-primary", () => {
      if (isConfigRetry) {
        row.status = "published";
        row.attempts = "6 次";
      } else if (isSnapshotValidation) {
        row.status = "draft";
        row.checksum = "校验通过";
      } else if (isRevocationDrain) {
        row.status = "complete";
        row.drain = "完成 · 0 in-flight";
        row.deletion = "删除排程中 · 30 天内";
      } else {
        row.status = "published";
      }
      content.closest("dialog").close();
      renderPage();
      const message = isConfigRetry
        ? "已模拟重试；manifest 状态已更新"
        : isSnapshotValidation
          ? "Snapshot 校验已通过；请再次操作以模拟发布"
          : isRevocationDrain
            ? "在线撤回已完成；物理删除仍在排程中"
            : `${resource.noun}已模拟发布`;
      showToast(message);
    });
    actions.append(confirm);
    content.append(note, actions);
    openDialog(`${action} · ${row.name}`, "本地模拟操作确认", content);
  }

  function resolveQuarantine(row) {
    const content = el("div", "detail-panel");
    const note = el("div", "callout");
    note.append(el("span", "", "ⓘ"), el("div", "", "原型只模拟隔离记录状态。实际财务处置必须由 finance-operator 授权，并在服务端事务中验证 reservation、币种与预算 scope。"));
    const actions = el("div", "dialog-actions");
    actions.append(makeButton("保留待处理", "button", () => content.closest("dialog").close()));
    actions.append(makeButton("模拟标记已复核", "button button-primary", () => {
      row.status = "complete";
      content.closest("dialog").close();
      renderPage();
      showToast("演示记录已标记为已完成；未更改任何账本");
    }));
    content.append(note, actions);
    openDialog("账务隔离复核", row.name, content);
  }

  function handleRoute() {
    const requested = location.hash.slice(1) || "dashboard";
    currentPage = requested === "dashboard" || config.resources[requested] ? requested : "dashboard";
    searchText = "";
    filterStatus = "all";
    const active = document.activeElement;
    renderPage();
    if (active && active.closest(".nav-list")) {
      document.querySelector(".page-header h1")?.focus({ preventScroll: true });
    }
  }

  window.addEventListener("hashchange", handleRoute);
  renderShell();
}
