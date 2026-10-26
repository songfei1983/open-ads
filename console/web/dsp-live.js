// dsp-live.js drives the DSP operations console against the real DSP admin
// API. Every page reads and writes through /api/dsp/admin/v1/* — the console
// service proxies these calls and injects the management token server side,
// so no credential ever reaches the browser and the UI has no other backend
// (no database, no Valkey, no side channel).
const app = document.querySelector("#app");
const API = "/api/dsp/admin/v1";
const MICROS_PER_UNIT = 1_000_000;

const state = {
  connected: false,
  onlineError: "",
  status: null,
  advertisers: [],
  campaigns: [],
  lineItems: [],
  creatives: [],
  budgets: [],
  quarantine: [],
};

const bannerSizes = [
  [728, 90],
  [300, 250],
  [320, 50],
];

const pages = {
  dashboard: { title: "投放总览", eyebrow: "总览", icon: "⌂", label: "投放总览" },
  campaigns: { title: "Campaign", eyebrow: "管理", icon: "▤", label: "Campaign", description: "广告主活动的投放时间窗草稿与发布状态；发布后由配置 manifest 扇出到 bidder。", apiPath: "campaigns" },
  "line-items": { title: "Line Item", eyebrow: "管理", icon: "◫", label: "Line Item", description: "Bid、预算、targeting 与素材关联；预算以 micros 记账（1 微 = 百万分之一 USD）。", apiPath: "line-items" },
  creatives: { title: "Creative 素材", eyebrow: "管理", icon: "▧", label: "Creative 素材", description: "上传静态图片素材（服务端校验格式/尺寸/hash），登记落地页并发布；仅 https 落地页可发布。", apiPath: "creatives" },
  budgets: { title: "预算与租约", eyebrow: "账本", icon: "＄", label: "预算与租约", description: "来自 DSP 管理 API 的真实预算账户视图（daily / lifetime scope、spent 与租约占用）。", apiPath: "budgets" },
  quarantine: { title: "账务隔离", eyebrow: "对账", icon: "⚑", label: "账务隔离", description: "迟到、冲突或校验失败的 notice 记录；处置经由 DSP 管理 API 幂等确认。", apiPath: "quarantine" },
};

const navOrder = ["dashboard", "campaigns", "line-items", "creatives", "budgets", "quarantine"];

let currentPage = location.hash.slice(1) || "dashboard";
if (!pages[currentPage]) currentPage = "dashboard";
let searchText = "";
let filterStatus = "all";
let toastTimer;

// ---------------------------------------------------------------------------
// API plumbing

async function apiFetch(path, options = {}) {
  let response;
  try {
    response = await fetch(`${API}/${path}`, {
      credentials: "omit",
      cache: "no-store",
      ...options,
      headers: { "X-Request-ID": crypto.randomUUID(), ...(options.headers || {}) },
    });
  } catch (networkError) {
    const failure = new Error("DSP 管理服务不可达（console 代理转发失败）");
    failure.code = "dsp_admin_unreachable";
    throw failure;
  }
  let body = null;
  try { body = await response.clone().json(); } catch { /* non-JSON body */ }
  if (!response.ok) {
    const failure = new Error(body?.message || `管理 API HTTP ${response.status}`);
    failure.code = body?.code || `http_${response.status}`;
    failure.status = response.status;
    throw failure;
  }
  return body;
}

function handleApiError(error) {
  if (error?.code === "version_conflict") {
    showToast("资源已被其他人修改（409 version_conflict），已刷新到最新版本");
    // Conflicts self-heal: reload the fresh server state and re-render.
    renderPage().catch(() => {});
    return true;
  }
  showToast(error?.message || "管理 API 调用失败");
  if (error?.code === "unauthenticated" || error?.status === 401 || error?.status === 503) {
    state.connected = false;
    state.onlineError = error.message;
  }
  return false;
}

// ---------------------------------------------------------------------------
// Shell

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function makeButton(label, className, action, ariaLabel) {
  const button = el("button", className, label);
  button.type = "button";
  if (ariaLabel) button.setAttribute("aria-label", ariaLabel);
  button.addEventListener("click", action);
  return button;
}

function renderShell() {
  app.replaceChildren();
  const shell = el("div", "shell");
  const sidebar = el("aside", "sidebar");
  sidebar.setAttribute("aria-label", "服务导航");

  const brand = el("a", "console-brand");
  brand.href = "./index.html";
  brand.setAttribute("aria-label", "Open Ads 首页");
  brand.innerHTML = '<span class="brand-mark" aria-hidden="true"><span></span><span></span><span></span></span><span>openads</span>';
  sidebar.append(brand);

  sidebar.append(el("div", "sidebar-label", "WORKSPACE"));
  const nav = el("nav", "nav-list");
  nav.setAttribute("aria-label", "DSP 工作区");
  for (const key of navOrder) {
    const anchor = el("a", "nav-link");
    anchor.href = `#${key}`;
    if (currentPage === key) anchor.setAttribute("aria-current", "page");
    anchor.append(el("span", "nav-icon", pages[key].icon), el("span", "", pages[key].label));
    nav.append(anchor);
  }
  sidebar.append(nav, el("div", "sidebar-spacer"), el("div", "sidebar-separator"));
  const meta = el("div", "side-meta");
  meta.append(el("div", "", "数据来源 · DSP Admin API"));
  meta.append(el("div", "", "通过 console 代理转发"));
  sidebar.append(meta);

  const mainShell = el("div", "main-shell");
  const topbar = el("header", "topbar");
  const crumbs = el("div", "breadcrumbs");
  crumbs.append(el("span", "", "Open Ads"), el("span", "", "/"), el("strong", "", "DSP"));
  const topActions = el("div", "topbar-actions");
  topActions.append(buildManifestChip());
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
}

// Live configuration state chip: current manifest, what the bidder actually
// loaded, and the outbox backlog. Refreshed with the page data.
function buildManifestChip() {
  const chip = el("span", "env-chip manifest-chip");
  if (!state.status) {
    chip.append(el("i"), document.createTextNode("配置状态 · 加载中"));
    return chip;
  }
  if (state.status.outbox_pending_count > 0) {
    chip.classList.add("manifest-pending");
    chip.append(el("i"), document.createTextNode(`配置发布中 · outbox 待确认 ${state.status.outbox_pending_count}`));
    return chip;
  }
  chip.append(el("i"), document.createTextNode(`manifest v${state.status.manifest_version} · bidder 已加载 v${loadedManifestNumber()}`));
  return chip;
}

function loadedManifestNumber() {
  const raw = state.status?.loaded_snapshot_version ?? "";
  const parsed = Number(raw);
  return Number.isFinite(parsed) && raw !== "" ? parsed : state.status?.manifest_version ?? 0;
}

// ---------------------------------------------------------------------------
// Data uplifting

async function refreshStatus() {
  state.status = await apiFetch("status");
}

async function refreshList(pageKey) {
  const path = pages[pageKey]?.apiPath;
  if (!path) return;
  const items = await apiFetch(path);
  const key = { campaigns: "campaigns", "line-items": "lineItems", creatives: "creatives", budgets: "budgets", quarantine: "quarantine" }[pageKey];
  state[key] = items;
}

async function refreshAll() {
  await Promise.all([
    refreshStatus(),
    apiFetch("advertisers").then((items) => { state.advertisers = items; }),
    refreshList("campaigns"),
    refreshList("line-items"),
    refreshList("creatives"),
    refreshList("budgets"),
    refreshList("quarantine"),
  ]);
  state.connected = true;
  state.onlineError = "";
}

// ---------------------------------------------------------------------------
// Formatting

function formatMicros(micros, currency = "USD") {
  const value = Number(micros) / MICROS_PER_UNIT;
  const digits = value >= 100 ? 2 : Math.abs(value) > 0 && Math.abs(value) < 0.01 ? 6 : 4;
  return `${currency} ${value.toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: digits })}`;
}

function formatIn(micros) {
  return (Number(micros) / MICROS_PER_UNIT).toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 6 });
}

function formatDay(iso) {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return date.toLocaleDateString("zh-CN");
}

function formatStamp(iso) {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return date.toLocaleString("zh-CN", { hour12: false, dateStyle: "short", timeStyle: "short" });
}

function localizeStatus(status) {
  const labels = {
    active: "运行中", published: "已发布", healthy: "健康", complete: "已完成",
    draft: "草稿", pending: "发布中", paused: "已暂停", open: "待处理",
    failed: "失败", resolved: "已处置",
  };
  return labels[status] || status || "—";
}

function shortHash(value) {
  if (!value) return "—";
  return `${String(value).slice(0, 10)}…${String(value).slice(-4)}`;
}

function advertiserName(id) {
  if (!id) return "—";
  return `adv ${String(id).slice(0, 8)}`;
}

function campaignName(id) {
  if (!id) return "—";
  return `cmp ${String(id).slice(0, 8)}`;
}

function targetingSummary(lineItem) {
  const parts = [];
  if (lineItem.targeting?.site_domains?.length) parts.push(lineItem.targeting.site_domains.join(", "));
  const sizes = (lineItem.targeting?.banner_sizes ?? []).map((size) => `${size.w}×${size.h}`);
  if (sizes.length) parts.push(sizes.join(" · "));
  if (lineItem.targeting?.page_path_prefixes?.length) parts.push(`路径 ${lineItem.targeting.page_path_prefixes.join(", ")}`);
  return parts.length ? parts.join(" · ") : "不限";
}

// ---------------------------------------------------------------------------
// Rendering

function renderPage() {
  const main = document.querySelector("#main-content");
  main.replaceChildren();
  const page = pages[currentPage];
  document.title = `${page.title} · DSP · Open Ads`;
  const header = el("section", "page-header");
  const titleGroup = el("div");
  const heading = el("h1", "", page.title);
  heading.tabIndex = -1;
  titleGroup.append(el("div", "eyebrow", `DSP / ${page.eyebrow}`), heading, el("p", "page-description", page.description || ""));
  header.append(titleGroup);
  const actions = el("div", "header-actions");
  if (currentPage === "campaigns") actions.append(makeButton("＋ 新建 Campaign", "button button-primary", () => openCampaignDialog()));
  if (currentPage === "line-items") actions.append(makeButton("＋ 新建 Line Item", "button button-primary", () => openLineItemDialog()));
  if (currentPage === "creatives") actions.append(makeButton("＋ 上传 Creative", "button button-primary", () => openCreativeDialog()));
  if (currentPage !== "dashboard") actions.append(makeButton("↻ 刷新", "button", () => loadAndRender(true)));
  header.append(actions);
  main.append(header);

  if (!state.connected) {
    main.append(offlineBanner());
    renderFooterInto(main);
    return;
  }
  const content = currentPage === "dashboard" ? renderDashboard() : renderResourcePage();
  main.append(content);
  renderFooterInto(main);
  const chip = document.querySelector(".manifest-chip");
  if (chip) chip.replaceWith(buildManifestChip());
}

function offlineBanner() {
  const note = el("div", "callout offline-callout");
  note.append(el("span", "", "⚠"));
  const copy = el("div");
  copy.append(el("strong", "", "DSP 管理 API 未连接："), document.createTextNode(state.onlineError || "请确认 DSP 服务在运行，且 console 配置了 DSP_ADMIN_URL / DSP_ADMIN_TOKEN。"));
  copy.append(makeButton("重试连接", "button", async () => {
    try {
      await refreshAll();
      renderShell();
      await renderPage();
    } catch (error) {
      handleApiError(error);
      renderPage();
    }
  }));
  note.append(copy);
  return note;
}

function renderFooterInto(main) {
  const footer = el("footer", "page-footer");
  footer.append(el("span", "", "Open Ads DSP Console"), el("span", "", "实时数据 · 仅通过 DSP 管理 API"));
  main.append(footer);
}

function renderDashboard() {
  const fragment = document.createDocumentFragment();
  const status = state.status ?? { manifest_version: 0, loaded_snapshot_version: "", outbox_pending_count: 0 };
  const stats = el("section", "stats-grid");
  stats.setAttribute("aria-label", "DSP 运行指标");
  const statDefs = [
    ["Live Campaign", String(state.campaigns.filter((c) => c.publish_status === "published").length), "已发布并由 bidder 拥有", "▤"],
    ["已发布素材", String(state.creatives.filter((c) => c.publish_status === "published").length), `${state.creatives.length} 个素材登记`, "▧"],
    ["配置 manifest", `v${status.manifest_version}`, `bidder 已加载 v${loadedManifestNumber()}`, "↻"],
    ["账务隔离待处置", String(state.quarantine.filter((q) => q.status === "open").length), "迟到/冲突 notice", "⚑"],
  ];
  for (const [label, value, foot, icon] of statDefs) {
    const card = el("article", "stat-card");
    const top = el("div", "stat-top");
    top.append(el("span", "", label), el("span", "stat-symbol", icon));
    card.append(top, el("div", "stat-value", value), el("div", "stat-foot", foot));
    stats.append(card);
  }
  fragment.append(stats);

  const content = el("section", "content-grid");
  const recent = el("section", "card");
  recent.append(cardHeader("最近发布的资源", "查看全部", () => { currentPage = "campaigns"; location.hash = "campaigns"; }));
  const table = el("table");
  table.append(makeTableHead([["资源", "资源"], ["状态", "状态"], ["版本", "版本"]]));
  const body = el("tbody");
  const recentRows = [
    ...state.campaigns.map((row) => ({ kind: "Campaign", name: campaignName(row.id), status: row.publish_status, version: row.version, page: "campaigns" })),
    ...state.creatives.map((row) => ({ kind: "Creative", name: shortHash(row.asset_sha256), status: row.publish_status, version: row.version, page: "creatives" })),
    ...state.lineItems.map((row) => ({ kind: "Line Item", name: row.targeting?.site_domains?.[0] ?? row.id, status: row.publish_status, version: row.version, page: "line-items" })),
  ].slice(-6).reverse();
  if (!recentRows.length) {
    const tr = el("tr");
    const td = el("td", "empty-table", "还没有已发布的资源——先在 Campaign / Creative 页创建并发布。");
    td.colSpan = 3;
    tr.append(td);
    body.append(tr);
  }
  for (const row of recentRows) {
    const tr = el("tr");
    const kindCell = el("td");
    kindCell.append(el("div", "cell-main", row.kind), el("div", "cell-sub", row.name));
    tr.append(kindCell);
    tr.append(el("td", "", localizeStatus(row.status)));
    const versionCell = el("td");
    versionCell.append(el("span", `status-pill ${row.status ?? "draft"}`, `v${row.version} · ${localizeStatus(row.status)}`));
    tr.append(versionCell);
    body.append(tr);
  }
  table.append(body);
  const wrap = el("div", "table-wrap");
  wrap.append(table);
  recent.append(wrap);
  content.append(recent);

  const health = el("section", "card");
  health.append(cardHeader("配置流水线", "刷新状态", () => loadAndRender(true)));
  const list = el("div");
  const rows = [
    ["Tenant", state.status?.tenant_id ?? "—"],
    ["Outbox 待确认", String(status.outbox_pending_count)],
    ["Bidder 加载版本", `v${loadedManifestNumber()}`],
    ["预算账户", `${state.budgets.length} 个 scope`],
  ];
  for (const [label, value] of rows) {
    const row = el("div", "mini-metric");
    row.append(el("span", "", label), el("strong", "", value));
    list.append(row);
  }
  health.append(list);
  content.append(health);
  fragment.append(content);
  return fragment;
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

function renderResourcePage() {
  const section = el("section");
  if (currentPage === "line-items") section.append(makeNote("首期边界", "支持 site.domain、page 路径前缀、banner 尺寸和预算；频控、pacing 与 segment targeting 暂不支持。", "ⓘ"));
  if (currentPage === "creatives") section.append(makeNote("素材链路", "上传后文件内容寻址存储（sha256）；Creative 只引用已登记的 asset_ref，发布时再次复核完整性。决策期按部署侧白名单（landing host=MIME，沙箱为 example.com=image/png）过滤，落地页请使用白名单内的 host。", "◈"));
  if (currentPage === "quarantine") section.append(renderQuarantineResource());

  const card = el("section", "card table-card");
  const wrap = el("div", "table-wrap");
  const table = el("table");
  const columns = currentColumns().concat([["actions", "操作"]]);
  table.append(makeTableHead(columns));
  const body = el("tbody");
  renderTableRows(body, columns);
  table.append(body);
  wrap.append(table);
  card.append(wrap);
  const footer = el("div", "table-footer");
  const countKey = { campaigns: "campaigns", "line-items": "lineItems", creatives: "creatives", budgets: "budgets", quarantine: "quarantine" }[currentPage];
  footer.append(el("span", "", `DSP 管理 API · ${state[countKey].length} 条记录`));
  const pagination = el("div", "pagination");
  pagination.append(el("span", "page-number current", "1"));
  footer.append(pagination);
  card.append(footer);
  section.append(card);
  return section;
}

function renderQuarantineResource() {
  if (state.quarantine.some((item) => item.status === "open")) return makeNote("对账提示", "隔离记录长期未处置会持续占用 dsp_notice_quarantine；确认业务侧无需重放后可标记 resolved。", "⚑");
  return el("div");
}

function makeNote(title, text, icon) {
  const note = el("div", "callout");
  note.append(el("span", "", icon), el("div"));
  note.lastChild.append(el("strong", "", `${title}：`), document.createTextNode(text));
  return note;
}

function currentColumns() {
  switch (currentPage) {
    case "campaigns": return [
      ["id", "Campaign"], ["advertiser", "Advertiser"], ["status", "状态"], ["period", "投放周期"], ["publish", "发布状态"],
    ];
    case "line-items": return [
      ["id", "Line Item"], ["campaign", "Campaign"], ["bid", "Bid CPM"], ["budget", "Daily / Lifetime"], ["targeting", "Targeting"], ["creatives", "素材"], ["publish", "发布状态"],
    ];
    case "creatives": return [
      ["id", "Asset"], ["format", "格式 / 尺寸"], ["landing", "落地页（https）"], ["publish", "发布状态"],
    ];
    case "budgets": return [
      ["lineItem", "Line Item"], ["scope", "Scope"], ["limit", "预算上限"], ["spent", "已消耗"], ["leased", "租约占用"], ["available", "可用"], ["status", "状态"],
    ];
    case "quarantine": return [
      ["id", "Reservation"], ["reason", "原因"], ["received", "接收时间"], ["status", "状态"],
    ];
    default: return [];
  }
}

function cellValue(key, row) {
  if (currentPage === "campaigns") {
    if (key === "advertiser") return advertiserName(row.advertiser_id);
    if (key === "period") return `${formatDay(row.starts_at)} → ${formatDay(row.ends_at)}`;
  }
  if (currentPage === "line-items") {
    if (key === "campaign") return campaignName(row.campaign_id);
    if (key === "bid") return formatMicros(row.bid_cpm_micros, row.currency);
    if (key === "budget") return `${formatIn(row.daily_budget_micros)} / ${formatIn(row.lifetime_budget_micros)} ${row.currency}`;
    if (key === "targeting") return targetingSummary(row);
    if (key === "creatives") return `${row.creative_ids?.length ?? 0} 个`;
  }
  if (currentPage === "creatives") {
    if (key === "format") return `${(row.format || "").split("/")[1]?.toUpperCase() || "IMG"} · ${row.width_px}×${row.height_px}`;
    if (key === "landing") return row.landing_url.replace(/^https?:\/\//, "");
    if (key === "id") return shortHash(row.asset_sha256);
  }
  if (currentPage === "budgets") {
    if (key === "lineItem") return row.line_item_id ? `${String(row.line_item_id).slice(0, 10)}…` : "—";
    if (key === "scope") return row.budget_scope === "daily" ? `Daily · ${row.period_start}` : "Lifetime";
    if (key === "limit") return formatMicros(row.budget_limit_micros, row.currency);
    if (key === "spent") return formatMicros(row.spent_micros, row.currency);
    if (key === "leased") return formatMicros(row.leased_micros, row.currency);
    if (key === "available") return formatMicros(row.available_micros, row.currency);
  }
  if (currentPage === "quarantine") {
    if (key === "id") return row.reservation_id ? `rsv ${shortHash(row.reservation_id)}` : shortHash(row.quarantine_id);
    if (key === "reason") return row.reason_code || row.outcome || "—";
    if (key === "received") return formatStamp(row.received_at);
  }
  if (key === "status") return localizeStatus(row.status);
  return row[key] ?? "—";
}

function publishCell(row) {
  const wrapper = el("td");
  if (!row.publish_status && "budget_scope" in row) { // budget rows have no publish state
    wrapper.append(el("span", `status-pill ${row.status}`, localizeStatus(row.status)));
    return wrapper;
  }
  const pillRow = el("div", "publish-cell");
  pillRow.append(el("span", `status-pill ${row.publish_status || "draft"}`, localizeStatus(row.publish_status || "draft")));
  if (row.config_version) {
    pillRow.append(el("span", "cell-sub mono", shortHash(row.config_version)));
    pillRow.lastChild.title = `config_version ${row.config_version}`;
  }
  wrapper.append(pillRow, el("div", "cell-sub", `v${row.version}`));
  return wrapper;
}

function makeTableRow(row, columns) {
  const tr = el("tr");
  for (const [key] of columns) {
    const td = el("td");
    if (key === "id") {
      const display = currentPage === "campaigns" ? campaignName(row.id) : currentPage === "line-items" ? `${row.currency === "USD" ? "bid " : ""}${String(row.id).slice(0, 10)}…` : String(row.id).slice(0, 12);
      td.append(el("div", "cell-main", display), el("div", "cell-sub mono", row.id));
    } else if (key === "publish") {
      const cell = publishCell(row);
      td.replaceChildren(...cell.childNodes);
    } else if (key === "status" && currentPage === "budgets") {
      td.append(el("span", `status-pill ${row.status}`, localizeStatus(row.status)));
    } else if (key === "status" && currentPage !== "budgets" && currentPage !== "quarantine") {
      td.append(el("span", `status-pill ${row.publish_status || row.status}`, localizeStatus(row.publish_status || row.status)));
    } else {
      td.textContent = cellValue(key, row);
    }
    tr.append(td);
  }
  const actions = el("div", "row-actions");
  if (currentPage === "campaigns" || currentPage === "line-items" || currentPage === "creatives") {
    actions.append(makeButton("✎", "icon-button", () => openEditDialog(row), `编辑 ${displayName(row)}`));
    actions.append(makeButton("↑", "icon-button icon-publish", () => openPublishDialog(row), `发布 ${displayName(row)}`));
  }
  if (currentPage === "quarantine" && row.status === "open") {
    actions.append(makeButton("✓", "icon-button", () => openResolveDialog(row), `处置 ${shortHash(row.quarantine_id)}`));
  }
  const actionCell = el("td");
  actionCell.append(actions);
  tr.append(actionCell);
  return tr;
}

function displayName(row) {
  if (currentPage === "campaigns") return campaignName(row.id);
  if (currentPage === "line-items") return String(row.campaign_id ? campaignName(row.campaign_id) : row.id);
  if (currentPage === "creatives") return shortHash(row.asset_sha256);
  return shortHash(row.id ?? row.quarantine_id);
}

function renderTableRows(body, columns) {
  const key = { campaigns: "campaigns", "line-items": "lineItems", creatives: "creatives", budgets: "budgets", quarantine: "quarantine" }[currentPage];
  let rows = state[key];
  if (filterStatus !== "all") {
    rows = rows.filter((row) => (row.publish_status || row.status) === filterStatus);
  }
  if (searchText) {
    const needle = searchText.toLowerCase();
    rows = rows.filter((row) => JSON.stringify(row).toLowerCase().includes(needle));
  }
  body.replaceChildren();
  if (!rows.length) {
    const tr = el("tr");
    const td = el("td", "empty-table");
    td.colSpan = columns.length;
    td.append(el("strong", "", "暂无记录"), document.createTextNode(" 通过页头按钮创建，或调整搜索条件。"));
    tr.append(td);
    body.append(tr);
    return;
  }
  for (const row of rows) body.append(makeTableRow(row, columns));
}

// ---------------------------------------------------------------------------
// Dialogs

function openDialog(title, description, content) {
  document.querySelectorAll("dialog:not([open])").forEach((closed) => closed.remove());
  const dialog = document.createElement("dialog");
  dialog.setAttribute("aria-labelledby", "modal-title");
  const titleNode = el("h2", "", title);
  titleNode.id = "modal-title";
  const headLeft = el("div");
  headLeft.append(titleNode, el("p", "", description));
  const close = makeButton("×", "dialog-close", () => dialog.close(), "关闭对话框");
  const head = el("div", "dialog-head");
  head.append(headLeft, close);
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

function makeSectionLabel(text) {
  return el("div", "section-label", text);
}

function makeDateTimeControl(value) {
  const input = el("input");
  input.type = "datetime-local";
  if (value) input.value = toDatetimeLocal(value);
  return input;
}

function toDatetimeLocal(iso) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  const pad = (n) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function toRFC3339(datetimeLocal) {
  if (!datetimeLocal) return null;
  const date = new Date(datetimeLocal);
  if (Number.isNaN(date.getTime())) return null;
  return date.toISOString();
}

// -- Publish confirmation -----------------------------------------------------

function openPublishDialog(row) {
  const kind = pages[currentPage].apiPath === "campaigns" ? "Campaign" : pages[currentPage].apiPath === "line-items" ? "Line Item" : "Creative";
  const content = el("div", "dialog-form");
  const summary = el("ul", "publish-summary");
  const items = [
    ["资源", displayName(row)],
    ["当前草稿版本", `v${row.version}`],
    ["发布状态", localizeStatus(row.publish_status || "draft")],
    ["上次 config_version", row.config_version ? shortHash(row.config_version) : "从未发布"],
  ];
  for (const [label, value] of items) {
    const li = el("li");
    li.append(el("span", "", label), el("strong", "", value));
    summary.append(li);
  }
  content.append(summary);
  const hint = el("p", "publish-hint");
  hint.textContent = "确认后将写入配置 manifest（outbox 待确认）；DSP 实例轮询至多 10 秒后快照生效，期间出价仍使用上一个已发布版本。";
  content.append(hint);
  const error = el("p", "form-error");
  error.setAttribute("role", "alert");
  error.hidden = true;
  const actions = el("div", "dialog-actions");
  actions.append(makeButton("取消", "button", () => dialog.close()));
  const submit = el("button", "button button-primary", `确认发布 v${row.version}`);
  submit.type = "submit";
  actions.append(submit);
  content.append(error, actions);
  const dialog = openDialog(`发布 ${kind}`, "发布是版本化但不可逆的审计操作；重复发布会生成新的 config_version。", content);
  submit.addEventListener("click", async () => {
    submit.disabled = true;
    submit.textContent = "发布中…";
    try {
      const result = await apiFetch(`${pages[currentPage].apiPath}/${row.id}/publish`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ version: row.version }),
      });
      dialog.close();
      showToast(`已提交发布 · config_version ${shortHash(result.config_version)}`);
      pollPublishOutcome(result.config_version);
    } catch (error) {
      submit.disabled = false;
      submit.textContent = `确认发布 v${row.version}`;
      if (handleApiError(error)) dialog.close();
      else {
        error.hidden = false;
        error.textContent = error.message;
      }
    }
  });
  void form;
}

async function pollPublishOutcome(configVersion) {
  for (let attempt = 0; attempt < 8; attempt += 1) {
    await new Promise((resolve) => setTimeout(resolve, attempt === 0 ? 600 : 2000));
    try {
      const status = await refreshStatus();
      const chip = document.querySelector(".manifest-chip");
      if (chip) chip.replaceWith(buildManifestChip());
      if (status.outbox_pending_count === 0 && status.manifest_version > 0) {
        showToast("发布已确认：bidder 实例将在 ≤10 秒内加载新快照");
        return;
      }
    } catch { /* keep polling; transient failures are expected */ }
  }
  showToast(`config ${shortHash(configVersion)} 仍在扇出（outbox 重试中），可稍后刷新确认`);
}

// -- Advertiser / Campaign ------------------------------------------------

function openCampaignDialog(existing) {
  const form = el("form", "dialog-form");
  form.noValidate = true;
  const grid = el("div", "form-grid");

  const advertiserRow = el("div", "form-field");
  advertiserRow.append(el("label", "", "Advertiser"));
  const advertiserSelect = el("select");
  advertiserSelect.name = "advertiser_id";
  advertiserSelect.required = !existing;
  const defaultOption = el("option", "", existing ? advertiserName(existing.advertiser_id) : "选择 Advertiser");
  defaultOption.value = "";
  advertiserSelect.append(defaultOption);
  for (const advertiser of state.advertisers) {
    const option = el("option", "", advertiserName(advertiser.id));
    option.value = advertiser.id;
    advertiserSelect.append(option);
  }
  advertiserRow.append(advertiserSelect);
  grid.append(advertiserRow);

  const statusField = el("div", "form-field");
  statusField.append(el("label", "", "状态"));
  const statusSelect = el("select");
  statusSelect.name = "status";
  for (const [value, label] of [["active", "active · 运行中"], ["paused", "paused · 暂停"]]) {
    const option = el("option", "", label);
    option.value = value;
    statusSelect.append(option);
  }
  statusSelect.value = existing?.status ?? "active";
  statusField.append(statusSelect);
  grid.append(statusField);

  const startField = el("div", "form-field");
  startField.append(el("label", "", "starts_at（UTC）"));
  const startInput = makeDateTimeControl(existing?.starts_at ?? new Date().toISOString());
  startInput.name = "starts_at";
  startInput.required = true;
  startField.append(startInput);
  grid.append(startField);

  const endField = el("div", "form-field");
  endField.append(el("label", "", "ends_at（可选，UTC）"));
  const endInput = makeDateTimeControl(existing?.ends_at ?? "");
  endInput.name = "ends_at";
  endField.append(endInput);
  grid.append(endField);

  if (!existing) {
    const extras = el("div", "form-field full");
    extras.append(makeButton("＋ 新建 Advertiser（active / USD）", "button", () => openAdvertiserDialog(advertiserSelect), "新建 Advertiser"));
    grid.append(extras);
  }

  const error = el("p", "form-error");
  error.setAttribute("role", "alert");
  error.hidden = true;
  const actions = el("div", "dialog-actions");
  actions.append(makeButton("取消", "button", () => dialog.close()));
  const submit = el("button", "button button-primary", existing ? "保存草稿" : "创建 Campaign");
  submit.type = "submit";
  actions.append(submit);
  form.append(grid, error, actions);
  const dialog = openDialog(existing ? "编辑 Campaign" : "新建 Campaign", "Campaign 管理 API 校验时间窗与状态；编辑按乐观锁提交当前版本。", form);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const payload = { status: statusSelect.value };
    const startsAt = toRFC3339(startInput.value);
    if (!startsAt) {
      showFormError(error, startInput, "starts_at 无效");
      return;
    }
    payload.starts_at = startsAt;
    const endsAt = toRFC3339(endInput.value);
    if (endsAt) payload.ends_at = endsAt;
    if (endInput.value && (!endsAt || new Date(endsAt) <= new Date(startsAt))) {
      showFormError(error, endInput, "ends_at 必须晚于 starts_at");
      return;
    }
    if (advertiserSelect.value) payload.advertiser_id = advertiserSelect.value;
    if (existing) payload.version = existing.version;
    submit.disabled = true;
    submit.textContent = existing ? "保存中…" : "创建中…";
    try {
      if (existing) {
        await apiFetch(`campaigns/${existing.id}`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
        showToast("Campaign 草稿已保存");
      } else {
        await apiFetch("campaigns", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
        showToast("Campaign 已创建（草稿）；发布后进入投放热路径");
      }
      dialog.close();
      await renderPage();
    } catch (apiError) {
      submit.disabled = false;
      submit.textContent = existing ? "保存草稿" : "创建 Campaign";
      if (!handleApiError(apiError)) {
        error.hidden = false;
        error.textContent = apiError.message;
      } else {
        dialog.close();
      }
    }
  });
}

function openAdvertiserDialog(select) {
  const form = el("form", "dialog-form");
  const actions = el("div", "dialog-actions");
  const statusSelect = el("select");
  statusSelect.name = "status";
  for (const value of ["active", "paused"]) {
    const option = el("option", "", value === "active" ? "active · 运行中" : "paused · 暂停");
    option.value = value;
    statusSelect.append(option);
  }
  const currencySelect = el("select");
  currencySelect.name = "default_currency";
  for (const value of ["USD", "JPY"]) {
    const option = el("option", "", value);
    option.value = value;
    currencySelect.append(option);
  }
  const grid = el("div", "form-grid");
  const statusField = el("div", "form-field");
  statusField.append(el("label", "", "状态"), statusSelect);
  const currencyField = el("div", "form-field");
  currencyField.append(el("label", "", "默认币种"), currencySelect);
  grid.append(statusField, currencyField);
  const error = el("p", "form-error");
  error.setAttribute("role", "alert");
  error.hidden = true;
  actions.append(makeButton("取消", "button", () => dialog.close()));
  const submit = el("button", "button button-primary", "创建 Advertiser");
  submit.type = "submit";
  actions.append(submit);
  form.append(grid, error, actions);
  const dialog = openDialog("新建 Advertiser", "Advertiser 仅承载状态与默认币种；管理 API 按当前租户分配 32 位 ID。", form);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    submit.disabled = true;
    try {
      const created = await apiFetch("advertisers", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ status: statusSelect.value, default_currency: currencySelect.value }),
      });
      dialog.close();
      const option = el("option", "", advertiserName(created.id));
      option.value = created.id;
      select.append(option);
      select.value = created.id;
      showToast(`Advertiser ${shortHash(created.id)} 已创建`);
    } catch (apiError) {
      submit.disabled = false;
      error.hidden = false;
      error.textContent = apiError.message;
    }
  });
}

// -- Line Item ------------------------------------------------------------

function openLineItemDialog(existing) {
  const form = el("form", "dialog-form");
  form.noValidate = true;
  const grid = el("div", "form-grid");

  const campaignField = el("div", "form-field");
  campaignField.append(el("label", "", "Campaign"));
  const campaignSelect = el("select");
  campaignSelect.name = "campaign_id";
  campaignSelect.required = !existing;
  if (!state.campaigns.length) {
    const empty = el("option", "", "（暂无 Campaign，请先创建）");
    empty.value = "";
    campaignSelect.append(empty);
  } else {
    const blank = el("option", "", existing ? campaignName(existing.campaign_id) : "选择 Campaign");
    blank.value = "";
    campaignSelect.append(blank);
    for (const campaign of state.campaigns) {
      const option = el("option", "", `${campaignName(campaign.id)} · v${campaign.version} · ${localizeStatus(campaign.publish_status || campaign.status)}`);
      option.value = campaign.id;
      campaignSelect.append(option);
    }
  }
  if (existing) campaignSelect.value = existing.campaign_id;
  campaignField.append(campaignSelect);
  grid.append(campaignField);

  const statusField = el("div", "form-field");
  statusField.append(el("label", "", "状态"));
  const statusSelect = el("select");
  statusSelect.name = "status";
  for (const [value, label] of [["active", "active · 运行中"], ["paused", "paused · 暂停"]]) {
    const option = el("option", "", label);
    option.value = value;
    statusSelect.append(option);
  }
  statusSelect.value = existing?.status ?? "active";
  statusField.append(statusSelect);
  grid.append(statusField);

  const currencyField = el("div", "form-field");
  currencyField.append(el("label", "", "币种"));
  const currencySelect = el("select");
  currencySelect.name = "currency";
  for (const value of ["USD", "JPY"]) {
    const option = el("option", "", value);
    option.value = value;
    currencySelect.append(option);
  }
  currencySelect.value = existing?.currency ?? "USD";
  currencyField.append(currencySelect);
  grid.append(currencyField);

  const bidField = el("div", "form-field");
  bidField.append(el("label", "", "Bid CPM micros"));
  const bidInput = el("input");
  bidInput.type = "number";
  bidInput.name = "bid_cpm_micros";
  bidInput.min = "1";
  bidInput.step = "1";
  bidInput.placeholder = "2500000 = 2.50 USD";
  if (existing?.bid_cpm_micros) bidInput.value = existing.bid_cpm_micros;
  bidInput.required = true;
  bidField.append(bidInput);
  grid.append(bidField);

  const microHint = el("small", "", "micros 记账：1_000_000 micros = 1 USD；预算同样以 micros 提交。");
  microHint.className = "form-field full";
  grid.append(microHint);

  const dailyField = el("div", "form-field");
  dailyField.append(el("label", "", "Daily budget micros"));
  const dailyInput = el("input");
  dailyInput.type = "number";
  dailyInput.name = "daily_budget_micros";
  dailyInput.min = "1";
  dailyInput.step = "1";
  dailyInput.placeholder = "1000000000 = 1000 USD";
  if (existing?.daily_budget_micros) dailyInput.value = existing.daily_budget_micros;
  dailyInput.required = true;
  dailyField.append(dailyInput);
  grid.append(dailyField);

  const lifetimeField = el("div", "form-field");
  lifetimeField.append(el("label", "", "Lifetime budget micros"));
  const lifetimeInput = el("input");
  lifetimeInput.type = "number";
  lifetimeInput.name = "lifetime_budget_micros";
  lifetimeInput.min = "1";
  lifetimeInput.step = "1";
  lifetimeInput.placeholder = "10000000000 = 10000 USD";
  if (existing?.lifetime_budget_micros) lifetimeInput.value = existing.lifetime_budget_micros;
  lifetimeInput.required = true;
  lifetimeField.append(lifetimeInput);
  grid.append(lifetimeField);

  // Targeting: banner sizes (checkbox list), site domains, page prefixes.
  grid.append(makeSectionLabel("Targeting"));
  const sizeField = el("div", "form-field full");
  sizeField.append(el("label", "", "Banner 尺寸（至少一个）"));
  const sizeList = el("div", "checkbox-list");
  for (const [width, height] of bannerSizes) {
    const id = `size-${width}-${height}`;
    const label = el("label", "checkbox-item");
    const checkbox = el("input");
    checkbox.type = "checkbox";
    checkbox.value = `${width}x${height}`;
    checkbox.id = id;
    checkbox.checked = (existing?.targeting?.banner_sizes ?? []).some((size) => size.w === width && size.h === height);
    label.append(checkbox, el("span", "", `${width} × ${height}`));
    sizeList.append(label);
  }
  sizeField.append(sizeList);
  grid.append(sizeField);

  const domainsField = el("div", "form-field");
  domainsField.append(el("label", "", "site_domains（逗号分隔，可空）"));
  const domainsInput = el("input");
  domainsInput.name = "site_domains";
  domainsInput.placeholder = "publisher.test, sakura.example";
  domainsInput.value = existing?.targeting?.site_domains?.join(", ") ?? "";
  domainsField.append(domainsInput);
  grid.append(domainsField);

  const prefixesField = el("div", "form-field");
  prefixesField.append(el("label", "", "page_path_prefixes（逗号分隔，可空）"));
  const prefixesInput = el("input");
  prefixesInput.name = "page_path_prefixes";
  prefixesInput.placeholder = "/home/, /article/";
  prefixesInput.value = existing?.targeting?.page_path_prefixes?.join(", ") ?? "";
  prefixesField.append(prefixesInput);
  grid.append(prefixesField);

  grid.append(makeSectionLabel("素材关联"));
  const creativeField = el("div", "form-field full");
  creativeField.append(el("label", "", "Creative（至少一个）"));
  const creativeList = el("div", "checkbox-list");
  if (!state.creatives.length) {
    creativeList.append(el("small", "", "暂无 Creative，请先在素材页上传。"));
  }
  for (const creative of state.creatives) {
    const id = `creative-${creative.id}`;
    const label = el("label", "checkbox-item");
    const checkbox = el("input");
    checkbox.type = "checkbox";
    checkbox.value = creative.id;
    checkbox.id = id;
    checkbox.checked = (existing?.creative_ids ?? []).includes(creative.id);
    label.append(
      checkbox,
      el("span", "", `${(creative.format || "").split("/")[1]?.toUpperCase() || "IMG"} ${creative.width_px}×${creative.height_px} · ${localizeStatus(creative.publish_status || "draft")}`),
    );
    creativeList.append(label);
  }
  creativeField.append(creativeList);
  grid.append(creativeField);

  const error = el("p", "form-error");
  error.setAttribute("role", "alert");
  error.hidden = true;
  const actions = el("div", "dialog-actions");
  actions.append(makeButton("取消", "button", () => dialog.close()));
  const submit = el("button", "button button-primary", existing ? "保存草稿" : "创建 Line Item");
  submit.type = "submit";
  actions.append(submit);
  form.append(grid, error, actions);
  const dialog = openDialog(existing ? "编辑 Line Item" : "新建 Line Item", "Bid、预算与 targeting 全部经 DSP 管理 API 校验；乐观锁以 version 冲突告警。", form);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const sizes = [...sizeList.querySelectorAll("input:checked")].map((box) => {
      const [w, h] = box.value.split("x");
      return { w: Number(w), h: Number(h) };
    });
    const domains = splitList(domainsInput.value);
    const prefixes = splitList(prefixesInput.value);
    const creativeIds = [...creativeList.querySelectorAll("input:checked")].map((box) => box.value);
    if (Number(bidInput.value) <= 0) return showFormError(error, bidInput, "Bid CPM micros 必须为正");
    if (Number(dailyInput.value) <= 0 || Number(lifetimeInput.value) <= 0) return showFormError(error, dailyInput, "daily / lifetime 预算必须为正数（micros）");
    if (existing && !campaignSelect.value) return showFormError(error, campaignSelect, "请选择 Campaign");
    if (sizes.length === 0) return showFormError(error, sizeList, "至少选择一个 banner 尺寸");
    if (creativeIds.length === 0) return showFormError(error, creativeList, "至少关联一个 Creative");
    const payload = {
      campaign_id: existing ? existing.campaign_id : campaignSelect.value,
      status: statusSelect.value,
      bid_cpm_micros: Number(bidInput.value),
      currency: currencySelect.value,
      daily_budget_micros: Number(dailyInput.value),
      lifetime_budget_micros: Number(lifetimeInput.value),
      targeting: {
        ...(domains.length ? { site_domains: domains } : {}),
        ...(prefixes.length ? { page_path_prefixes: prefixes } : {}),
        ...(sizes.length ? { banner_sizes: sizes } : {}),
      },
      creative_ids: creativeIds,
      ...(existing ? { version: existing.version } : {}),
    };
    submit.disabled = true;
    submit.textContent = existing ? "保存中…" : "创建中…";
    try {
      if (existing) {
        await apiFetch(`line-items/${existing.id}`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
        showToast("Line Item 草稿已保存");
      } else {
        await apiFetch("line-items", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
        showToast("Line Item 已创建（草稿）");
      }
      dialog.close();
      await renderPage();
    } catch (apiError) {
      submit.disabled = false;
      submit.textContent = existing ? "保存草稿" : "创建 Line Item";
      if (!handleApiError(apiError)) {
        error.hidden = false;
        error.textContent = apiError.message;
      } else {
        dialog.close();
      }
    }
  });
}

function splitList(raw) {
  return raw.split(",").map((part) => part.trim()).filter(Boolean);
}

// -- Creative -------------------------------------------------------------

function openCreativeDialog(existing) {
  const form = el("form", "dialog-form");
  form.noValidate = true;
  const grid = el("div", "form-grid");

  const fileField = el("div", "form-field full");
  fileField.append(el("label", "", existing ? "替换素材文件（可选，PNG / JPEG / WebP，≤10 MB）" : "素材文件（PNG / JPEG / WebP，≤10 MB）"));
  const fileInput = el("input");
  fileInput.type = "file";
  fileInput.name = "file";
  fileInput.accept = "image/png,image/jpeg,image/webp";
  if (!existing) fileInput.required = true;
  const fileHint = el("small", "", "浏览器本地计算 SHA-256，服务端解码校验后再入库（内容寻址，重复上传自动复用）。落地页 host 需在部署侧白名单内（沙箱 example.com）。");
  fileField.append(fileInput, fileHint);
  grid.append(fileField);

  const landingField = el("div", "form-field full");
  landingField.append(el("label", "", "Landing URL（必须 https）"));
  const landingInput = el("input");
  landingInput.type = "url";
  landingInput.name = "landing_url";
  landingInput.placeholder = "https://example.com/landing";
  landingInput.value = existing?.landing_url ?? "";
  landingInput.required = true;
  landingField.append(landingInput);
  grid.append(landingField);

  const statusField = el("div", "form-field");
  statusField.append(el("label", "", "状态"));
  const statusSelect = el("select");
  statusSelect.name = "status";
  for (const [value, label] of [["active", "active · 运行中"], ["paused", "paused · 暂停"]]) {
    const option = el("option", "", label);
    option.value = value;
    statusSelect.append(option);
  }
  statusSelect.value = existing?.status ?? "active";
  statusField.append(statusSelect);
  grid.append(statusField);

  const error = el("p", "form-error");
  error.setAttribute("role", "alert");
  error.hidden = true;
  const actions = el("div", "dialog-actions");
  actions.append(makeButton("取消", "button", () => dialog.close()));
  const submit = el("button", "button button-primary", existing ? "保存 Creative" : "上传并登记 Creative");
  submit.type = "submit";
  actions.append(submit);
  form.append(grid, error, actions);
  const dialog = openDialog(existing ? "编辑 Creative" : "上传 Creative", "文件先经 /admin/v1/creative-assets 校验入库，再登记 Creative 草稿；发布时服务端复核 hash。", form);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    submit.disabled = true;
    submit.textContent = "处理中…";
    const file = fileInput.files[0] ?? null;
    if (file && file.size > 10 * 1024 * 1024) {
      submit.disabled = false;
      submit.textContent = existing ? "保存 Creative" : "上传并登记 Creative";
      return showFormError(error, fileInput, "文件超过 10 MB 限制");
    }
    try {
      let assetRef = existing?.asset_ref;
      let assetSHA = existing?.asset_sha256;
      if (file) {
        const bytes = await file.arrayBuffer();
        const digest = await crypto.subtle.digest("SHA-256", bytes);
        const sha = [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
        const metadata = await uploadAsset(file, sha);
        assetRef = metadata.asset_ref;
        assetSHA = metadata.actual_sha256;
        if (existing && existing.asset_sha256 && existing.asset_sha256 !== sha) {
          showToast("素材已替换：Creative 指向新的内容寻址对象");
        }
      }
      if (!assetRef) {
        submit.disabled = false;
        submit.textContent = "保存 Creative";
        return showFormError(error, fileInput, "需要素材文件才能登记 Creative");
      }
      const payload = {
        status: statusSelect.value,
        asset_ref: assetRef,
        landing_url: landingInput.value.trim(),
        ...(existing ? { version: existing.version } : {}),
      };
      if (existing) {
        await apiFetch(`creatives/${existing.id}`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
        showToast("Creative 草稿已保存");
      } else {
        await apiFetch("creatives", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
        showToast(`Creative 已登记（${shortHash(assetSHA)}）`);
      }
      dialog.close();
      await renderPage();
    } catch (apiError) {
      submit.disabled = false;
      submit.textContent = existing ? "保存 Creative" : "上传并登记 Creative";
      if (!handleApiError(apiError)) {
        error.hidden = false;
        error.textContent = apiError.message;
      } else {
        dialog.close();
      }
    }
  });
}

async function uploadAsset(file, expectedSha) {
  const form = new FormData();
  form.append("file", file, file.name);
  form.append("expected_sha256", expectedSha);
  return apiFetch("creative-assets", { method: "POST", body: form, headers: {} });
}

// -- Quarantine -----------------------------------------------------------

function openResolveDialog(row) {
  const content = el("div", "dialog-form");
  const summary = el("ul", "publish-summary");
  for (const [label, value] of [
    ["隔离记录", shortHash(row.quarantine_id)],
    ["Reservation", row.reservation_id ? shortHash(row.reservation_id) : "—"],
    ["原因", row.reason_code || row.outcome || "—"],
  ]) {
    const li = el("li");
    li.append(el("span", "", label), el("strong", "", value));
    summary.append(li);
  }
  content.append(summary, el("p", "publish-hint", "标记 resolved 表示业务侧已确认该 notice 不可重放；操作经由管理 API 幂等完成。"));
  const actionState = el("div", "dialog-actions");
  actionState.append(makeButton("取消", "button", () => dialog.close()));
  const submit = el("button", "button button-primary", "标记 resolved");
  actionState.append(submit);
  content.append(actionState);
  const dialog = openDialog("处置账务隔离记录", "确认后该记录从 open 列表移除；处置属于审计操作。", content);
  submit.addEventListener("click", async () => {
    submit.disabled = true;
    submit.textContent = "处置中…";
    try {
      await apiFetch(`quarantine/${row.quarantine_id}/resolve`, { method: "POST", body: null, headers: {} });
      dialog.close();
      showToast("隔离记录已处置");
      await renderPage();
    } catch (error) {
      submit.disabled = false;
      submit.textContent = "标记 resolved";
      handleApiError(error);
    }
  });
}

// -- Shared form helpers ----------------------------------------------------

function showFormError(errorNode, focusNode, message) {
  errorNode.textContent = message;
  errorNode.hidden = false;
  if (focusNode) focusNode.focus();
  return false;
}

// ---------------------------------------------------------------------------
// Wiring

async function loadAndRender(force = false) {
  try {
    if (force || !state.connected) await refreshAll();
    else await Promise.all([refreshStatus(), refreshList(currentPage)]);
  } catch (error) {
    state.connected = false;
    state.onlineError = error?.message || "连接失败";
  }
  renderShell();
  await renderPage();
}

window.addEventListener("hashchange", () => {
  const target = location.hash.slice(1);
  currentPage = pages[target] ? target : "dashboard";
  searchText = "";
  filterStatus = "all";
  loadAndRender();
});

loadAndRender();
