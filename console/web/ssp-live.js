// SSP live console. Every value comes from the SSP management API through the
// console proxy (/api/ssp/*); the browser never sees the admin bearer token.
const API = "/api/ssp";
const app = document.querySelector("#app");

const pages = {
  overview: { label: "运营总览", icon: "⌂", title: "SSP 运营总览", eyebrow: "Overview" },
  publishers: { label: "Publisher", icon: "◉", title: "Publisher", eyebrow: "Publishers" },
  properties: { label: "Property", icon: "◈", title: "媒体资源", eyebrow: "Properties" },
  policies: { label: "Auction Policy", icon: "⚖", title: "拍卖策略", eyebrow: "Auction policies" },
  placements: { label: "Placement", icon: "▦", title: "广告位", eyebrow: "Placements" },
  supply: { label: "Supply 接入", icon: "⇥", title: "Supply 接入", eyebrow: "Supply endpoints" },
  "dsp-endpoints": { label: "DSP 连接", icon: "⇄", title: "DSP 连接", eyebrow: "DSP endpoints" },
  outbox: { label: "配置发布", icon: "↻", title: "配置发布", eyebrow: "Config outbox" },
  runtime: { label: "实例状态", icon: "◷", title: "实例与快照", eyebrow: "Runtime" },
};
const navOrder = Object.keys(pages);

const state = {
  publishers: [],
  properties: [],
  policies: [],
  placements: [],
  supply: [],
  dsp: [],
  pending: [],
  failed: [],
  instances: [],
  connected: false,
  loadedAt: "",
  error: "",
};

let currentPage = pages[location.hash.slice(1)] ? location.hash.slice(1) : "overview";
let toastTimer = 0;
const toastState = { message: "" };

// -- primitives --------------------------------------------------------------

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

function paintToast() {
  const toast = document.querySelector("#console-toast");
  if (!toast) return;
  toast.textContent = toastState.message;
  toast.hidden = !toastState.message;
}

function showToast(message) {
  toastState.message = message;
  paintToast();
  window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => {
    toastState.message = "";
    paintToast();
  }, 3600);
}

function short(value, size = 10) {
  if (!value) return "—";
  return value.length > size ? `${value.slice(0, size)}…` : value;
}

function fmtTime(value) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString("zh-CN", { hour12: false });
}

function statusPill(status) {
  const map = {
    active: ["active", "active"],
    published: ["published", "已发布"],
    paused: ["paused", "paused"],
    draft: ["draft", "draft"],
    pending: ["pending", "待发布"],
    failed: ["failed", "失败"],
    ready: ["complete", "ready"],
    stale: ["paused", "stale"],
  };
  const [className, label] = map[status] ?? ["draft", status || "—"];
  return el("span", `status-pill ${className}`, label);
}

async function apiFetch(path, options = {}) {
  let response;
  try {
    response = await fetch(`${API}${path}`, {
      cache: "no-store",
      credentials: "omit",
      ...options,
      headers: { "Content-Type": "application/json", ...(options.headers || {}) },
    });
  } catch {
    throw Object.assign(new Error("SSP 服务不可达（console 代理失败）"), { code: "ssp_unreachable" });
  }
  let body = null;
  try { body = await response.clone().json(); } catch { /* non-JSON */ }
  if (!response.ok) {
    throw Object.assign(new Error(body?.message || body?.code || `SSP HTTP ${response.status}`), {
      code: body?.code || `http_${response.status}`,
      status: response.status,
    });
  }
  return body;
}

function handleError(error) {
  showToast(error?.message || "调用失败");
  if (error?.status === 401 || error?.status === 503) {
    state.connected = false;
    state.error = error.message;
  }
  render();
}

// -- shell -------------------------------------------------------------------

function serviceLink(key, label) {
  const anchor = el("a", "", label);
  anchor.href = `./${key}.html`;
  if (key === "ssp") anchor.setAttribute("aria-current", "page");
  return anchor;
}

function renderShell() {
  app.replaceChildren();
  const shell = el("div", "shell");
  const sidebar = el("aside", "sidebar");
  sidebar.setAttribute("aria-label", "服务导航");

  const brand = el("a", "console-brand");
  brand.href = "./index.html";
  brand.innerHTML = '<span class="brand-mark" aria-hidden="true"><span></span><span></span><span></span></span><span>openads</span>';
  sidebar.append(brand);

  const serviceSelect = el("div", "service-select");
  serviceSelect.append(el("span", "service-glyph", "S"), el("span"), el("span", "chevron", "⌄"));
  serviceSelect.lastElementChild.previousElementSibling.append(el("strong", "", "SSP 供给平台"), el("small", "", "Live workspace"));
  sidebar.append(serviceSelect, el("div", "sidebar-label", "WORKSPACE"));

  const nav = el("nav", "nav-list");
  nav.setAttribute("aria-label", "SSP 工作区");
  for (const key of navOrder) {
    const anchor = el("a", "nav-link");
    anchor.href = `#${key}`;
    if (currentPage === key) anchor.setAttribute("aria-current", "page");
    anchor.append(el("span", "nav-icon", pages[key].icon), el("span", "", pages[key].label));
    nav.append(anchor);
  }
  sidebar.append(nav, el("div", "sidebar-spacer"), el("div", "sidebar-separator"));

  const switcher = el("div", "service-switch");
  switcher.append(serviceLink("ssp", "SSP"), serviceLink("dsp", "DSP"), serviceLink("dmp", "DMP"));
  sidebar.append(el("div", "sidebar-label", "SERVICE CONSOLES"), switcher, el("div", "sidebar-separator"));
  const meta = el("div", "side-meta");
  const mode = el("div");
  mode.append(document.createTextNode("运行模式 · "), el("b", "", "Live API"));
  meta.append(mode, el("div", "", "真实数据 · 仅经 SSP 管理 API"));
  sidebar.append(meta);

  const mainShell = el("div", "main-shell");
  const topbar = el("header", "topbar");
  const crumbs = el("div", "breadcrumbs");
  crumbs.append(el("span", "", "Open Ads"), el("span", "", "/"), el("strong", "", "SSP"));
  const topActions = el("div", "topbar-actions");
  const chip = el("span", "env-chip manifest-chip");
  chip.append(el("i"), document.createTextNode(state.connected
    ? `已发布 ${state.placements.filter((p) => p.published_version > 0).length} 个广告位 · 待发布 ${state.pending.length}`
    : "SSP API · 加载中"));
  const env = el("span", "env-chip");
  env.append(el("i"), document.createTextNode("测试环境"));
  const user = el("div", "user-chip");
  user.append(el("span", "user-avatar", "S"), el("span", "", "Sandbox Operator"));
  topActions.append(chip, env, user);
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
  paintToast();
  return main;
}

function pageHeader(key, actionLabel, action) {
  const section = el("section", "page-header");
  const group = el("div");
  group.append(el("div", "eyebrow", `SSP / ${pages[key].eyebrow}`), el("h1", "", pages[key].title));
  section.append(group);
  const actions = el("div", "header-actions");
  if (action) actions.append(makeButton(actionLabel, "button button-primary", action));
  actions.append(makeButton("↻ 刷新", "button", () => loadAll()));
  section.append(actions);
  return section;
}

function statCard(label, value, hint, symbol) {
  const card = el("article", "stat-card");
  const head = el("div");
  head.append(el("span", "", label), el("span", "stat-symbol", symbol));
  card.append(head, el("strong", "", String(value)), el("small", "", hint));
  return card;
}

function tableCard(title, description, columns, rows) {
  const card = el("section", "card table-card");
  const header = el("div", "card-header");
  const left = el("div");
  left.append(el("h2", "", title), el("p", "", description));
  header.append(left);
  card.append(header);
  const wrap = el("div", "table-wrap");
  const table = el("table");
  const thead = el("thead");
  const headRow = el("tr");
  for (const column of columns) headRow.append(el("th", "", column));
  thead.append(headRow);
  const tbody = el("tbody");
  if (!rows.length) {
    const row = el("tr");
    const cell = el("td", "", "暂无记录");
    cell.colSpan = columns.length;
    row.append(cell);
    tbody.append(row);
  }
  for (const row of rows) {
    const tr = el("tr");
    for (const cell of row) tr.append(cell);
    tbody.append(tr);
  }
  table.append(thead, tbody);
  wrap.append(table);
  card.append(wrap);
  return card;
}

function cell(text) { return el("td", "", text); }
function nodeCell(...nodes) {
  const td = el("td");
  for (const node of nodes) if (node) td.append(node);
  return td;
}

function note(title, text, symbol = "ⓘ") {
  const box = el("div", "callout");
  box.append(el("strong", "", `${symbol} ${title}`), el("span", "", text));
  return box;
}

function nameOf(list, id) {
  const item = list.find((candidate) => candidate.id === id);
  return item ? item.name : short(id);
}

// -- dialogs -----------------------------------------------------------------

function openDialog(title, description, content) {
  document.querySelectorAll("dialog:not([open])").forEach((closed) => closed.remove());
  const dialog = document.createElement("dialog");
  dialog.setAttribute("aria-labelledby", "modal-title");
  const heading = el("h2", "", title);
  heading.id = "modal-title";
  const left = el("div");
  left.append(heading, el("p", "", description));
  const close = makeButton("×", "dialog-close", () => dialog.close(), "关闭对话框");
  const head = el("div", "dialog-head");
  head.append(left, close);
  dialog.append(head, content);
  dialog.addEventListener("close", () => dialog.remove(), { once: true });
  dialog.addEventListener("click", (event) => { if (event.target === dialog) dialog.close(); });
  document.body.append(dialog);
  dialog.showModal();
  close.focus();
  return dialog;
}

function field(labelText, input) {
  const wrapper = el("div", "form-field");
  wrapper.append(el("label", "", labelText), input);
  return wrapper;
}

function textInput(name, value = "", placeholder = "") {
  const input = el("input");
  input.name = name;
  input.value = value ?? "";
  if (placeholder) input.placeholder = placeholder;
  return input;
}

function passwordInput(name, placeholder = "") {
  const input = textInput(name, "", placeholder);
  input.type = "password";
  input.autocomplete = "new-password";
  return input;
}

function numberInput(name, value = "") {
  const input = el("input");
  input.name = name;
  input.type = "number";
  input.value = value ?? "";
  return input;
}

function selectInput(name, options, value) {
  const select = el("select");
  select.name = name;
  for (const [optionValue, label] of options) {
    const option = el("option", "", label);
    option.value = optionValue;
    select.append(option);
  }
  select.value = value ?? options[0][0];
  return select;
}

function formError() {
  const error = el("p", "form-error");
  error.setAttribute("role", "alert");
  error.hidden = true;
  return error;
}

function showFormError(node, message) {
  node.textContent = message;
  node.hidden = false;
}

function statusOptions() {
  return [["active", "active"], ["paused", "paused"], ["draft", "draft"]];
}

// -- API ---------------------------------------------------------------------

async function loadAll() {
  try {
    const [publishers, properties, policies, placements, supply, dsp, pending, failed, instances] = await Promise.all([
      apiFetch("/admin/v1/publishers"),
      apiFetch("/admin/v1/properties"),
      apiFetch("/admin/v1/auction-policies"),
      apiFetch("/admin/v1/placements"),
      apiFetch("/admin/v1/supply-endpoints"),
      apiFetch("/admin/v1/dsp-endpoints"),
      apiFetch("/admin/v1/config-outbox?status=pending"),
      apiFetch("/admin/v1/config-outbox?status=failed"),
      apiFetch("/admin/v1/runtime/instances"),
    ]);
    Object.assign(state, {
      publishers: publishers || [], properties: properties || [], policies: policies || [],
      placements: placements || [], supply: supply || [], dsp: dsp || [],
      pending: pending || [], failed: failed || [], instances: instances || [],
      connected: true, error: "", loadedAt: new Date().toISOString(),
    });
  } catch (error) {
    state.connected = false;
    state.error = error.message;
    showToast(error.message);
  }
  render();
}

async function publish(resource, item) {
  try {
    const result = await apiFetch(`/admin/v1/${resource}/${item.id}:publish`, {
      method: "POST",
      body: JSON.stringify({ version: item.version, reason: "console" }),
    });
    showToast(`已进入发布队列 · config ${result.config_version}`);
    await waitForPublish(resource, item.id, item.version);
  } catch (error) {
    handleError(error);
  }
}

async function waitForPublish(resource, id, version) {
  for (let attempt = 0; attempt < 15; attempt += 1) {
    await new Promise((resolve) => window.setTimeout(resolve, 400));
    try {
      const items = await apiFetch(`/admin/v1/${resource}`);
      const latest = (items || []).find((candidate) => candidate.id === id);
      const instances = await apiFetch("/admin/v1/runtime/instances");
      const synced = (instances || []).filter((instance) => instance.ready && !instance.stale);
      if (latest && latest.published_version >= version && synced.length > 0) {
        showToast(`配置已发布 · v${latest.published_version} · ${synced.length} 个实例已同步`);
        break;
      }
    } catch {
      break;
    }
  }
  await loadAll();
}

// -- overview ----------------------------------------------------------------

function renderOverview(main) {
  main.append(pageHeader("overview"));
  main.append(note("配置边界", "热路径只读取已发布快照；草稿修改不会进入竞价。所有页面数据来自 SSP 管理 API。"));
  const ready = state.instances.filter((instance) => instance.ready && !instance.stale).length;
  const stats = el("section", "stats-grid");
  stats.append(
    statCard("Publisher", state.publishers.length, "已登记供给方", "◉"),
    statCard("Placement", state.placements.length, `${state.placements.filter((p) => p.published_version > 0).length} 已发布`, "▦"),
    statCard("DSP 连接", state.dsp.length, `${state.dsp.filter((d) => d.status === "active").length} active`, "⇄"),
    statCard("实例", state.instances.length, `${ready} ready · ${state.pending.length} 待发布`, "◷"),
  );
  main.append(stats);
  main.append(tableCard("待发布配置", "outbox 事件在 Valkey manifest 写入成功后才标记完成",
    ["事件", "资源", "版本", "创建时间", "状态"],
    [...state.pending, ...state.failed].slice(0, 8).map((event) => [
      nodeCell(el("code", "", short(event.event_id, 12))),
      cell(`${event.aggregate_type} · ${short(event.aggregate_id, 10)}`),
      cell(`v${event.aggregate_version}`),
      cell(fmtTime(event.created_at)),
      nodeCell(statusPill(event.failed_attempts >= 5 ? "failed" : "pending")),
    ])));
  main.append(tableCard("实例同步", "超过 10 秒未同步的实例会从 readyz 摘除",
    ["实例", "已加载", "观测到", "状态", "心跳"],
    state.instances.map((instance) => [
      nodeCell(el("code", "", short(instance.instance_id, 14))),
      cell(instance.manifest_version ? `v${instance.manifest_version}` : "—"),
      cell(instance.observed_version ? `v${instance.observed_version}` : "—"),
      nodeCell(statusPill(instance.ready && !instance.stale ? "ready" : "stale")),
      cell(instance.heartbeat_unix ? fmtTime(new Date(instance.heartbeat_unix * 1000)) : "—"),
    ])));
}

// -- resource pages ----------------------------------------------------------

function versionCell(item) {
  const published = item.published_version > 0 ? `v${item.published_version}` : "未发布";
  return nodeCell(
    el("code", "", published),
    el("span", "cell-sub", ` · draft v${item.version}`),
    item.published_version === item.version ? el("span", "status-pill published", "已同步") : null,
  );
}

function actionCell(resource, item, extra) {
  const actions = el("div", "row-actions");
  actions.append(makeButton("编辑", "button button-small", () => openResourceDialog(resource, item)));
  if (extra) actions.append(extra);
  const publishButton = makeButton(
    item.published_version === item.version ? "已发布" : "发布",
    "button button-small button-primary",
    () => publish(resource, item),
  );
  publishButton.disabled = item.published_version === item.version;
  actions.append(publishButton);
  return nodeCell(actions);
}

function renderResourcePage(main, resource) {
  const definitions = {
    publishers: {
      title: "Publisher", create: "新建 Publisher",
      description: "供给方是 tenant 内的资源所有者；暂停后其下广告位不再参与竞价。",
      columns: ["Publisher", "ID", "状态", "版本", "操作"],
      rows: () => state.publishers.map((item) => [
        nodeCell(el("strong", "", item.name)), nodeCell(el("code", "", short(item.id, 14))),
        nodeCell(statusPill(item.status)), versionCell(item), actionCell("publishers", item),
      ]),
    },
    properties: {
      title: "媒体资源", create: "新建 Property",
      description: "Property 绑定 Publisher 与外部 site/app 标识；Supply 入口据此校验供给身份。",
      columns: ["Property", "Publisher", "类型", "外部 ID", "状态", "版本", "操作"],
      rows: () => state.properties.map((item) => [
        nodeCell(el("strong", "", item.name)), cell(nameOf(state.publishers, item.publisher_id)),
        cell(item.type), nodeCell(el("code", "", item.external_id)), nodeCell(statusPill(item.status)),
        versionCell(item), actionCell("properties", item),
      ]),
    },
    policies: {
      title: "拍卖策略", create: "新建策略",
      description: "首期仅支持 at=1、dsp_priority_then_ssp_hash、strict floor 与 exact-match 币种。",
      columns: ["策略", "at", "Tie breaker", "状态", "版本", "操作"],
      rows: () => state.policies.map((item) => [
        nodeCell(el("strong", "", item.name)), cell(String(item.at)), nodeCell(el("code", "", item.tie_breaker)),
        nodeCell(statusPill(item.status)), versionCell(item), actionCell("auction-policies", item),
      ]),
    },
    placements: {
      title: "广告位", create: "新建 Placement",
      description: "尺寸、floor、币种与策略在发布时编译进热路径快照。",
      columns: ["Placement", "Property", "尺寸", "Floor", "策略", "状态", "版本", "操作"],
      rows: () => state.placements.map((item) => [
        nodeCell(el("strong", "", item.name)), cell(nameOf(state.properties, item.property_id)),
        cell(`${item.width} × ${item.height}`),
        nodeCell(el("code", "", `${(item.floor_cpm_micros / 1_000_000).toFixed(2)} ${item.currency}`)),
        cell(nameOf(state.policies, item.auction_policy_id)), nodeCell(statusPill(item.status)),
        versionCell(item), actionCell("placements", item),
      ]),
    },
    supply: {
      title: "Supply 接入", create: "新建 Supply endpoint",
      description: "Supply 凭据只在创建/轮换时写入；界面永不回显已保存的 secret。",
      columns: ["Endpoint", "Property", "协议", "凭据", "限流", "状态", "版本", "操作"],
      rows: () => state.supply.map((item) => [
        nodeCell(el("strong", "", item.name)), cell(nameOf(state.properties, item.property_id)),
        cell(item.protocol_version), nodeCell(statusPill(item.credential_configured ? "active" : "draft")),
        cell(item.rate_limit_per_second ? `${item.rate_limit_per_second}/s` : "不限"),
        nodeCell(statusPill(item.status)), versionCell(item), actionCell("supply-endpoints", item),
      ]),
    },
    "dsp-endpoints": {
      title: "DSP 连接", create: "新建 DSP endpoint",
      description: "回调 host 白名单用于 SSRF 防护；priority 只在同价时参与 tie-break。",
      columns: ["DSP", "Bid host", "Priority", "超时", "回调白名单", "状态", "版本", "操作"],
      rows: () => state.dsp.map((item) => [
        nodeCell(el("strong", "", item.name)), nodeCell(el("code", "", hostOf(item.bid_url))),
        cell(String(item.priority)), cell(`${item.timeout_ms} ms`),
        nodeCell(el("code", "", (item.callback_hosts || []).join(", "))), nodeCell(statusPill(item.status)),
        versionCell(item), actionCell("dsp-endpoints", item, makeButton("白名单", "button button-small", () => openWhitelist(item))),
      ]),
    },
  };
  const definition = definitions[resource];
  main.append(pageHeader(resource, definition.create, () => openResourceDialog(resource, null)));
  main.append(note(definition.title, definition.description));
  main.append(tableCard(definition.title, `${definition.rows().length} 条记录`, definition.columns, definition.rows()));
}

function hostOf(value) {
  try { return new URL(value).host; } catch { return value || "—"; }
}

function originsText(origins) { return (origins || []).join(", "); }

function parseOrigins(value) {
  return String(value || "").split(",").map((item) => item.trim()).filter(Boolean);
}

function openResourceDialog(resource, existing) {
  const form = el("form", "dialog-form");
  const grid = el("div", "form-grid");
  const error = formError();
  let payloadBuilder;
  let title = existing ? "编辑" : "新建";
  let description = "保存后仍是 draft；点击发布并通过校验后才进入热路径。";

  if (resource === "publishers") {
    const name = textInput("name", existing?.name ?? "");
    const status = selectInput("status", statusOptions(), existing?.status ?? "active");
    grid.append(field("名称", name), field("状态", status));
    payloadBuilder = () => ({ name: name.value.trim(), status: status.value });
  } else if (resource === "properties") {
    const publisher = selectInput("publisher_id", state.publishers.map((item) => [item.id, item.name]), existing?.publisher_id ?? state.publishers[0]?.id);
    const name = textInput("name", existing?.name ?? "");
    const type = selectInput("type", [["site", "site"], ["app", "app"]], existing?.type ?? "site");
    const external = textInput("external_id", existing?.external_id ?? "", "publisher.test");
    const status = selectInput("status", statusOptions(), existing?.status ?? "active");
    grid.append(field("Publisher", publisher), field("名称", name), field("类型", type),
      field("外部 ID / 域名", external), field("状态", status));
    payloadBuilder = () => ({ publisher_id: publisher.value, name: name.value.trim(), type: type.value,
      external_id: external.value.trim(), status: status.value });
  } else if (resource === "auction-policies") {
    const name = textInput("name", existing?.name ?? "");
    grid.append(field("名称", name));
    grid.append(field("at", (() => { const input = numberInput("at", "1"); input.disabled = true; return input; })()));
    grid.append(field("Tie breaker", el("span", "mono", "dsp_priority_then_ssp_hash")));
    grid.append(field("Floor enforcement", el("span", "mono", "strict")));
    grid.append(field("Currency policy", el("span", "mono", "exact_match")));
    payloadBuilder = () => ({ name: name.value.trim(), at: 1, tie_breaker: "dsp_priority_then_ssp_hash",
      floor_enforcement: "strict", currency_policy: "exact_match" });
  } else if (resource === "placements") {
    const property = selectInput("property_id", state.properties.map((item) => [item.id, item.name]), existing?.property_id ?? state.properties[0]?.id);
    const policy = selectInput("auction_policy_id", state.policies.map((item) => [item.id, item.name]), existing?.auction_policy_id ?? state.policies[0]?.id);
    const name = textInput("name", existing?.name ?? "");
    const width = numberInput("width", existing?.width ?? 728);
    const height = numberInput("height", existing?.height ?? 90);
    const floor = numberInput("floor", existing ? (existing.floor_cpm_micros / 1_000_000).toFixed(2) : "1.20");
    const currency = textInput("currency", existing?.currency ?? "USD");
    const privacy = selectInput("privacy_policy", [["consent_required", "consent_required"], ["no_identity", "no_identity"]], existing?.privacy_policy ?? "consent_required");
    const origins = textInput("allowed_origins", originsText(existing?.allowed_origins), "https://publisher.test");
    const status = selectInput("status", statusOptions(), existing?.status ?? "active");
    grid.append(field("Property", property), field("Auction policy", policy), field("名称", name),
      field("宽", width), field("高", height), field("Floor CPM (USD)", floor), field("币种", currency),
      field("隐私策略", privacy), field("允许的页面 Origin（逗号分隔）", origins), field("状态", status));
    payloadBuilder = () => ({ property_id: property.value, auction_policy_id: policy.value,
      name: name.value.trim(), media_type: "banner", width: Number(width.value), height: Number(height.value),
      floor_cpm_micros: Math.round(Number(floor.value) * 1_000_000), currency: currency.value.trim().toUpperCase(),
      privacy_policy: privacy.value, allowed_origins: parseOrigins(origins.value), status: status.value });
  } else if (resource === "supply-endpoints") {
    const property = selectInput("property_id", state.properties.map((item) => [item.id, item.name]), existing?.property_id ?? state.properties[0]?.id);
    const name = textInput("name", existing?.name ?? "");
    const credential = passwordInput("credential", existing ? "留空保持不变" : "至少 24 个字符");
    const origins = textInput("allowed_origins", originsText(existing?.allowed_origins), "https://publisher.test");
    const rate = numberInput("rate_limit_per_second", existing?.rate_limit_per_second ?? 100);
    const status = selectInput("status", statusOptions(), existing?.status ?? "active");
    grid.append(field("Property", property), field("名称", name),
      field(existing ? "轮换 Supply 凭据（可选）" : "Supply 凭据", credential),
      field("允许的页面 Origin（逗号分隔）", origins), field("每秒限流（0 为不限）", rate), field("状态", status));
    payloadBuilder = () => {
      const payload = { property_id: property.value, name: name.value.trim(), protocol_version: "openrtb2.6",
        allowed_origins: parseOrigins(origins.value), rate_limit_per_second: Number(rate.value), status: status.value };
      if (credential.value.trim()) payload.credential = credential.value.trim();
      return payload;
    };
  } else if (resource === "dsp-endpoints") {
    const name = textInput("name", existing?.name ?? "");
    const bidURL = textInput("bid_url", existing?.bid_url ?? "", "https://dsp.test/openrtb2/bid");
    const hosts = textInput("callback_hosts", (existing?.callback_hosts || []).join(", "), "dsp.test");
    const priority = numberInput("priority", existing?.priority ?? 10);
    const timeout = numberInput("timeout_ms", existing?.timeout_ms ?? 50);
    const status = selectInput("status", statusOptions(), existing?.status ?? "active");
    grid.append(field("名称", name), field("Bid URL", bidURL), field("回调 host 白名单（逗号分隔）", hosts),
      field("Priority", priority), field("Timeout (ms)", timeout), field("状态", status));
    payloadBuilder = () => ({ name: name.value.trim(), bid_url: bidURL.value.trim(), openrtb_profile: "openrtb2.6",
      callback_hosts: parseOrigins(hosts.value), priority: Number(priority.value), timeout_ms: Number(timeout.value), status: status.value });
  }

  const actions = el("div", "dialog-actions");
  actions.append(makeButton("取消", "button", () => dialog.close()));
  const submit = makeButton(existing ? "保存新版本" : "创建", "button button-primary", () => {});
  submit.type = "submit";
  actions.append(submit);
  form.append(grid, error, actions);
  const dialog = openDialog(`${title} ${pages[resource]?.label ?? ""}`, description, form);

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    submit.disabled = true;
    try {
      const payload = payloadBuilder();
      if (existing) {
        payload.version = existing.version;
        await apiFetch(`/admin/v1/${resource}/${existing.id}`, { method: "PUT", body: JSON.stringify(payload) });
        showToast(`${pages[resource]?.label ?? resource} 已保存为 draft v${existing.version + 1}`);
      } else {
        await apiFetch(`/admin/v1/${resource}`, { method: "POST", body: JSON.stringify(payload) });
        showToast(`${pages[resource]?.label ?? resource} 已创建为 draft`);
      }
      dialog.close();
      await loadAll();
    } catch (error) {
      submit.disabled = false;
      showFormError(error, error.message);
    }
  });
}

async function openWhitelist(endpoint) {
  const content = el("div");
  const error = formError();
  content.append(error);
  const dialog = openDialog("DSP Creative 白名单", "只读视图：白名单唯一写入路径是 DSP → SSP 内部同步 API。", content);
  try {
    const result = await apiFetch(`/admin/v1/dsp-endpoints/${endpoint.id}/creative-whitelist`);
    const rows = (result.whitelist || []).map((entry) => [
      nodeCell(el("code", "", short(entry.creative_id, 12))),
      nodeCell(el("code", "", short(entry.asset_ref, 22))),
      cell(`${entry.width_px} × ${entry.height_px}`),
      cell(entry.mime_type),
      cell(entry.landing_url_host),
      nodeCell(statusPill(entry.status)),
      cell(`v${entry.version}`),
    ]);
    content.append(tableCard("Creative 白名单", `${rows.length} 条记录`,
      ["Creative", "Asset", "尺寸", "MIME", "Landing host", "状态", "版本"], rows));
  } catch (err) {
    showFormError(error, err.message);
  }
  return dialog;
}

// -- outbox / runtime --------------------------------------------------------

function renderOutbox(main) {
  main.append(pageHeader("outbox"));
  main.append(note("发布语义", "outbox 事件只有在 Valkey snapshot 与 manifest 都持久成功后才标记 published；失败达到 5 次后需人工重试。"));
  const rows = [...state.pending, ...state.failed].map((event) => [
    nodeCell(el("code", "", short(event.event_id, 14))),
    cell(`${event.aggregate_type} · ${short(event.aggregate_id, 10)}`),
    cell(`v${event.aggregate_version}`),
    cell(fmtTime(event.created_at)),
    cell(event.failed_attempts ? `${event.failed_attempts} 次` : "—"),
    nodeCell(statusPill(event.failed_attempts >= 5 ? "failed" : "pending")),
    nodeCell(makeButton("重试", "button button-small", () => retryOutbox(event)), el("span", "cell-sub", event.last_error_sanitized || "")),
  ]);
  main.append(tableCard("配置事件", "pending + failed", ["事件", "资源", "版本", "创建时间", "失败", "状态", "操作"], rows));
}

async function retryOutbox(event) {
  try {
    await apiFetch(`/admin/v1/config-outbox/${event.event_id}:retry`, { method: "POST", body: JSON.stringify({}) });
    showToast(`事件 ${short(event.event_id, 8)} 已重试`);
    await loadAll();
  } catch (error) {
    handleError(error);
  }
}

function renderRuntime(main) {
  main.append(pageHeader("runtime"));
  main.append(note("扇出观察", "实例每 1 秒读取 manifest；落后超过 10 秒会从 readyz 摘除并持续追赶。"));
  main.append(tableCard("实例", `${state.instances.length} 个实例`,
    ["实例", "manifest", "observed", "ready", "心跳", "陈旧"],
    state.instances.map((instance) => [
      nodeCell(el("code", "", short(instance.instance_id, 16))),
      cell(instance.manifest_version ? `v${instance.manifest_version}` : "—"),
      cell(instance.observed_version ? `v${instance.observed_version}` : "—"),
      nodeCell(statusPill(instance.ready ? "ready" : "stale")),
      cell(instance.heartbeat_unix ? fmtTime(new Date(instance.heartbeat_unix * 1000)) : "—"),
      cell(instance.stale ? "是" : "否"),
    ])));
}

// -- render ------------------------------------------------------------------

function render() {
  const main = renderShell();
  if (!state.connected && state.error) {
    main.append(pageHeader(currentPage));
    main.append(note("SSP API 不可用", state.error, "⚠"));
    return;
  }
  switch (currentPage) {
    case "overview":
      renderOverview(main);
      break;
    case "outbox":
      renderOutbox(main);
      break;
    case "runtime":
      renderRuntime(main);
      break;
    default:
      renderResourcePage(main, currentPage);
  }
}

window.addEventListener("hashchange", () => {
  currentPage = pages[location.hash.slice(1)] ? location.hash.slice(1) : "overview";
  render();
});

render();
loadAll();
