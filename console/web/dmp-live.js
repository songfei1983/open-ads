// DMP live console. Every value on this page comes from the DMP API through the
// console proxy (/api/dmp/*, admin token injected server side). The browser
// never sees a credential, a subject token of a real person, or raw identity.

const API = "/api/dmp";
const app = document.querySelector("#app");

const pages = {
  overview: { label: "总览", icon: "▦", title: "DMP 总览", eyebrow: "Overview" },
  providers: { label: "Provider", icon: "◫", title: "数据提供方", eyebrow: "Providers" },
  segments: { label: "Segment", icon: "◈", title: "受众定义", eyebrow: "Segments" },
  snapshots: { label: "Snapshot", icon: "⇪", title: "快照导入与发布", eyebrow: "Snapshots" },
  revocations: { label: "撤回", icon: "⚑", title: "撤回与门禁", eyebrow: "Revocations" },
  retention: { label: "保留策略", icon: "⌛", title: "保留与物理删除", eyebrow: "Retention" },
};
const navOrder = Object.keys(pages);

const state = {
  providers: [],
  namespaces: [],
  segments: [],
  snapshots: [],
  publications: [],
  revocations: [],
  retention: null,
  lastSweep: null,
  connected: false,
  error: "",
};

let currentPage = pages[location.hash.slice(1)] ? location.hash.slice(1) : "overview";
let toastTimer = 0;

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

// The shell is rebuilt on every refresh, which replaces the toast node. Keeping
// the message in state makes it survive that re-render.
const toastState = { message: "" };

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
  }, 3200);
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
    retired: ["paused", "retired"],
    importing: ["pending", "导入中"],
    verified: ["draft", "已校验"],
    published: ["published", "已发布"],
    failed: ["failed", "失败"],
    denied: ["failed", "已拒绝"],
    complete: ["complete", "完成"],
    pending: ["pending", "待处理"],
    completed: ["complete", "已完成"],
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
    throw Object.assign(new Error("DMP 服务不可达（console 代理失败）"), { code: "dmp_unreachable" });
  }
  let body = null;
  try { body = await response.clone().json(); } catch { /* non-JSON */ }
  if (!response.ok) {
    throw Object.assign(new Error(body?.message || body?.code || `DMP HTTP ${response.status}`), {
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
  renderPage();
}

// -- shell -------------------------------------------------------------------

function serviceLink(key, label) {
  const anchor = el("a", "", label);
  anchor.href = `./${key}.html`;
  if (key === "dmp") anchor.setAttribute("aria-current", "page");
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
  serviceSelect.append(el("span", "service-glyph", "M"), el("span"), el("span", "chevron", "⌄"));
  serviceSelect.lastElementChild.previousElementSibling.append(el("strong", "", "DMP 数据平台"), el("small", "", "Live workspace"));
  sidebar.append(serviceSelect);

  sidebar.append(el("div", "sidebar-label", "WORKSPACE"));
  const nav = el("nav", "nav-list");
  nav.setAttribute("aria-label", "DMP 工作区");
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
  meta.append(mode, el("div", "", "真实数据 · 仅经 DMP API"));
  sidebar.append(meta);

  const mainShell = el("div", "main-shell");
  const topbar = el("header", "topbar");
  const crumbs = el("div", "breadcrumbs");
  crumbs.append(el("span", "", "Open Ads"), el("span", "", "/"), el("strong", "", "DMP"));
  const topActions = el("div", "topbar-actions");
  const chip = el("span", "env-chip manifest-chip");
  chip.append(el("i"), document.createTextNode(
    state.connected ? `发布快照 ${state.publishedCount} 个 · 待处理撤回 ${state.pendingRevocations}` : "DMP API · 加载中"));
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
}

// -- page scaffolding --------------------------------------------------------

function pageHeader(pageKey, actionLabel, action) {
  const section = el("section", "page-header");
  const group = el("div");
  group.append(el("div", "eyebrow", `DMP / ${pages[pageKey].eyebrow}`), el("h1", "", pages[pageKey].title));
  section.append(group);
  const actions = el("div", "header-actions");
  if (action) actions.append(makeButton(actionLabel, "button button-primary", action));
  actions.append(makeButton("↻ 刷新", "button", () => loadAndRender(true)));
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

function tableCard(title, description, columns, rows, linkLabel, linkAction) {
  const card = el("section", "card table-card");
  const header = el("div", "card-header");
  const left = el("div");
  left.append(el("h2", "", title), el("p", "", description));
  header.append(left);
  if (linkAction) header.append(makeButton(linkLabel, "card-link", linkAction));
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
  for (const node of nodes) {
    if (!node) continue;
    td.append(node);
  }
  return td;
}

// -- overview ----------------------------------------------------------------

function renderOverview(main) {
  main.append(pageHeader("overview"));
  main.append(note("隐私边界", "本页只显示定义、快照计数与撤回状态；DMP 不向控制台返回原始身份、subject token 或 profile 明细。", "ⓘ"));

  const published = state.publications.length;
  const failed = state.snapshots.filter((s) => s.status === "failed").length;
  const importing = state.snapshots.filter((s) => s.status === "importing").length;
  const pending = state.revocations.filter((r) => r.physical_delete_status === "pending").length;

  const stats = el("section", "stats-grid");
  stats.append(
    statCard("Provider", state.providers.length, "已登记数据提供方", "◫"),
    statCard("Segment", state.segments.length, "生效中的受众定义", "◈"),
    statCard("快照", state.snapshots.length, `${published} 当前服务 · ${importing} 导入中 · ${failed} 失败`, "⇪"),
    statCard("撤回", state.revocations.length, `${pending} 待物理删除`, "⚑"),
  );
  main.append(stats);

  const current = new Set(state.publications.map((publication) => publication.snapshot_id));
  const recent = [...state.snapshots]
    .sort((a, b) => String(b.created_at).localeCompare(String(a.created_at)))
    .slice(0, 6);
  main.append(tableCard("最近快照", "导入 → 校验 → 发布；未发布快照不可解析", ["快照", "Provider / Namespace", "状态", "行数", "来源"], recent.map((snapshot) => [
    nodeCell(el("code", "", short(snapshot.snapshot_id, 16)), current.has(snapshot.snapshot_id) ? el("span", "status-pill published", "当前服务") : null),
    cell(`${snapshot.provider_id} / ${snapshot.namespace}`),
    nodeCell(statusPill(snapshot.status)),
    cell(String(snapshot.stored_rows)),
    cell(snapshot.source_version),
  ]), "查看快照", () => { location.hash = "#snapshots"; }));

  main.append(note("门禁语义", "解析只读当前已发布快照；撤回在 Valkey deny gate 中原子阻断新 admission，gate 状态不确定时 DMP 返回 503 而不是返回受众。", "⌁"));
}

function note(title, text, icon) {
  const callout = el("div", "callout");
  const body = el("div");
  body.append(el("strong", "", `${title}：`), document.createTextNode(text));
  callout.append(el("span", "", icon), body);
  return callout;
}

// -- definitions -------------------------------------------------------------

function definitionTable(pageKey) {
  if (pageKey === "providers") {
    return tableCard("Provider", "namespace 创建后不可变更；引用必须为 active", ["ID", "显示名", "Namespace", "许可版本", "状态", "版本", ""], state.providers.map((provider) => [
      nodeCell(el("code", "", provider.id)),
      cell(provider.display_name),
      cell(provider.namespace),
      cell(provider.license_version),
      nodeCell(statusPill(provider.status)),
      cell(`v${provider.version}`),
      nodeCell(makeButton("✎", "icon-button", () => openProviderDialog(provider), `编辑 ${provider.id}`)),
    ]));
  }
  return tableCard("Segment", "segment 定义版本；导入时要求 provider 为 active", ["ID", "名称", "Provider", "状态", "版本", ""], state.segments.map((segment) => [
    nodeCell(el("code", "", segment.id)),
    cell(segment.name),
    cell(segment.provider_id),
    nodeCell(statusPill(segment.status)),
    cell(`v${segment.version}`),
    nodeCell(makeButton("✎", "icon-button", () => openSegmentDialog(segment), `编辑 ${segment.id}`)),
  ]));
}

function renderDefinitions(main, pageKey) {
  main.append(pageHeader(pageKey, pageKey === "providers" ? "＋ 新建 Provider" : "＋ 新建 Segment",
    pageKey === "providers" ? () => openProviderDialog() : () => openSegmentDialog()));
  if (!state.namespaces.length) {
    main.append(note("先建 namespace", "Provider 必须引用一个 active 的 identity namespace，请先通过管理 API 创建 namespace。", "ⓘ"));
  }
  main.append(definitionTable(pageKey));
}

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

function openProviderDialog(existing) {
  const form = el("form", "dialog-form");
  const grid = el("div", "form-grid");
  const idInput = textInput("id", existing?.id ?? "", "dmp-provider-02");
  if (existing) idInput.disabled = true;
  const nameInput = textInput("display_name", existing?.display_name ?? "");
  const namespaceSelect = selectInput("namespace", state.namespaces.map((ns) => [ns.id, ns.id]), existing?.namespace ?? state.namespaces[0]?.id);
  if (existing) namespaceSelect.disabled = true;
  const licenseInput = textInput("license_version", existing?.license_version ?? "lic-2026.10");
  const statusSelect = selectInput("status", [["active", "active"], ["retired", "retired"]], existing?.status ?? "active");
  grid.append(field("Provider ID", idInput), field("显示名", nameInput), field("Namespace", namespaceSelect),
    field("许可版本", licenseInput), field("状态", statusSelect));
  const error = formError();
  const actions = el("div", "dialog-actions");
  actions.append(makeButton("取消", "button", () => dialog.close()));
  const submit = makeButton(existing ? "保存新版本" : "创建 Provider", "button button-primary", () => {});
  submit.type = "submit";
  actions.append(submit);
  form.append(grid, error, actions);
  const dialog = openDialog(existing ? "编辑 Provider" : "新建 Provider",
    "每次保存生成一个新的不可变版本；namespace 创建后不可变更。", form);

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    submit.disabled = true;
    try {
      const payload = {
        display_name: nameInput.value.trim(),
        namespace: existing?.namespace ?? namespaceSelect.value,
        license_version: licenseInput.value.trim(),
        status: statusSelect.value,
      };
      if (existing) {
        payload.version = existing.version;
        await apiFetch(`/admin/v1/providers/${existing.id}`, { method: "PUT", body: JSON.stringify(payload) });
        showToast(`Provider ${existing.id} 已保存为新版本`);
      } else {
        payload.id = idInput.value.trim();
        await apiFetch("/admin/v1/providers", { method: "POST", body: JSON.stringify(payload) });
        showToast(`Provider ${payload.id} 已创建`);
      }
      dialog.close();
      await loadAndRender(true);
    } catch (error) {
      submit.disabled = false;
      showFormError(error, error.message);
    }
  });
}

function openSegmentDialog(existing) {
  const form = el("form", "dialog-form");
  const grid = el("div", "form-grid");
  const idInput = textInput("id", existing?.id ?? "", "seg-sports-fans");
  if (existing) idInput.disabled = true;
  const nameInput = textInput("name", existing?.name ?? "");
  const providerSelect = selectInput("provider_id", state.providers.map((p) => [p.id, `${p.id} · ${p.status}`]), existing?.provider_id ?? state.providers[0]?.id);
  if (existing) providerSelect.disabled = true;
  const descriptionInput = textInput("description", existing?.description ?? "");
  const statusSelect = selectInput("status", [["active", "active"], ["retired", "retired"]], existing?.status ?? "active");
  grid.append(field("Segment ID", idInput), field("名称", nameInput), field("Provider", providerSelect),
    field("描述", descriptionInput), field("状态", statusSelect));
  const error = formError();
  const actions = el("div", "dialog-actions");
  actions.append(makeButton("取消", "button", () => dialog.close()));
  const submit = makeButton(existing ? "保存新版本" : "创建 Segment", "button button-primary", () => {});
  submit.type = "submit";
  actions.append(submit);
  form.append(grid, error, actions);
  const dialog = openDialog(existing ? "编辑 Segment" : "新建 Segment",
    "segment 只承载定义；成员关系来自已校验并发布的快照。", form);

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    submit.disabled = true;
    try {
      const payload = {
        name: nameInput.value.trim(),
        description: descriptionInput.value.trim(),
        status: statusSelect.value,
        provider_id: existing?.provider_id ?? providerSelect.value,
      };
      if (existing) {
        payload.version = existing.version;
        await apiFetch(`/admin/v1/segments/${existing.id}`, { method: "PUT", body: JSON.stringify(payload) });
        showToast(`Segment ${existing.id} 已保存为新版本`);
      } else {
        payload.id = idInput.value.trim();
        await apiFetch("/admin/v1/segments", { method: "POST", body: JSON.stringify(payload) });
        showToast(`Segment ${payload.id} 已创建`);
      }
      dialog.close();
      await loadAndRender(true);
    } catch (error) {
      submit.disabled = false;
      showFormError(error, error.message);
    }
  });
}

// -- snapshots ---------------------------------------------------------------

function renderSnapshots(main) {
  main.append(pageHeader("snapshots", "＋ 新建导入", () => openImportDialog()));
  main.append(note("导入约束", "只接受 64 位小写 hex 的伪名 token；segment 必须是该 provider 的 active 定义。行数/checksum/重复率任一不符则校验失败，旧快照继续服务；快照一旦发布即封版，撤回只减行、不再重新校验。", "⇪"));

  const current = new Set(state.publications.map((publication) => publication.snapshot_id));
  main.append(tableCard("快照", "导入 → 校验 → 发布 → 可解析", ["快照", "Provider / Namespace", "来源", "状态", "行数", "重复", "创建时间", ""],
    [...state.snapshots].sort((a, b) => String(b.created_at).localeCompare(String(a.created_at))).map((snapshot) => [
      nodeCell(el("code", "", short(snapshot.snapshot_id, 16)), current.has(snapshot.snapshot_id) ? el("span", "status-pill published", "当前服务") : null),
      cell(`${snapshot.provider_id} / ${snapshot.namespace}`),
      cell(snapshot.source_version),
      nodeCell(statusPill(snapshot.status)),
      nodeCell(
        el("span", "", String(snapshot.stored_rows)),
        snapshot.purged_rows > 0 ? el("span", "status-pill paused", `已撤回 ${snapshot.purged_rows}`) : null,
      ),
      cell(String(snapshot.duplicate_rows)),
      cell(fmtTime(snapshot.created_at)),
      nodeCell(
        snapshot.status === "importing"
          ? makeButton("✓", "icon-button", () => verifySnapshot(snapshot), `校验 ${snapshot.snapshot_id}`)
          : null,
        snapshot.status === "verified" || snapshot.status === "importing"
          ? makeButton("↑", "icon-button", () => confirmPublish(snapshot), `发布 ${snapshot.snapshot_id}`)
          : null,
        snapshot.status === "published" || snapshot.status === "retired"
          ? null
          : makeButton("🗑", "icon-button", () => confirmDelete(snapshot), `删除 ${snapshot.snapshot_id}`),
        makeButton("⋯", "icon-button", () => showSnapshotDetails(snapshot), `查看 ${snapshot.snapshot_id}`),
      ),
    ])));
}

async function verifySnapshot(snapshot) {
  try {
    const job = await apiFetch(`/internal/v1/snapshots/${snapshot.snapshot_id}/verify`, { method: "POST" });
    if (job.status === "verified") showToast("校验通过：行数、checksum 与重复率均符合预期");
    else showToast(`校验未通过：${job.failure_reason || job.status}`);
    await loadAndRender(true);
  } catch (error) {
    if (error.code === "verification_failed") showToast(`校验失败：${error.message}`);
    else handleError(error);
    await loadAndRender(true);
  }
}

function confirmPublish(snapshot) {
  const content = el("div", "dialog-form");
  const list = el("ul", "detail-list");
  for (const [label, value] of [
    ["快照", snapshot.snapshot_id],
    ["Provider / Namespace", `${snapshot.provider_id} / ${snapshot.namespace}`],
    ["行数", `${snapshot.stored_rows} / ${snapshot.expected_rows}`],
    ["状态", snapshot.status],
  ]) {
    const item = el("li");
    item.append(el("span", "", label), el("strong", "", String(value)));
    list.append(item);
  }
  content.append(list, note("发布语义", "发布只会切换该 provider/namespace 的解析指针；未通过校验的快照无法发布，已发布快照不会被删除。", "⇪"));
  const actions = el("div", "dialog-actions");
  const dialog = openDialog("确认发布快照", "发布会立即影响在线受众解析。", content);
  actions.append(makeButton("取消", "button", () => dialog.close()));
  const submit = makeButton("确认发布", "button button-primary", async () => {
    submit.disabled = true;
    try {
      await apiFetch(`/internal/v1/snapshots/${snapshot.snapshot_id}/publish`, { method: "POST" });
      dialog.close();
      showToast("快照已发布，解析将使用该快照");
      await loadAndRender(true);
    } catch (error) {
      submit.disabled = false;
      showToast(error.message);
    }
  });
  actions.append(submit);
  content.append(actions);
}

function confirmDelete(snapshot) {
  const content = el("div", "dialog-form");
  content.append(note("删除不可回滚", "删除只移除该快照的 membership 行；当前服务快照会被拒绝。撤回 token 的物理删除由保留策略执行。", "🗑"));
  const actions = el("div", "dialog-actions");
  const dialog = openDialog("删除快照", `${snapshot.snapshot_id} · ${snapshot.stored_rows} 行`, content);
  actions.append(makeButton("取消", "button", () => dialog.close()));
  const submit = makeButton("确认删除", "button button-danger", async () => {
    submit.disabled = true;
    try {
      const result = await apiFetch(`/internal/v1/snapshots/${snapshot.snapshot_id}/delete`, { method: "POST" });
      dialog.close();
      showToast(`已删除 ${result.deleted_rows} 行`);
      await loadAndRender(true);
    } catch (error) {
      submit.disabled = false;
      showToast(error.message);
    }
  });
  actions.append(submit);
  content.append(actions);
}

function showSnapshotDetails(snapshot) {
  const content = el("div", "dialog-form");
  const list = el("ul", "detail-list");
  for (const [key, label] of [
    ["snapshot_id", "快照"], ["provider_id", "Provider"], ["namespace", "Namespace"],
    ["source_version", "来源版本"], ["schema_version", "Schema"], ["status", "状态"],
    ["expected_rows", "预期行数"], ["stored_rows", "已存行数"], ["purged_rows", "已撤回行"],
    ["submitted_rows", "提交行数"], ["duplicate_rows", "重复行"], ["expected_checksum", "预期 checksum"],
    ["failure_reason", "失败原因"],
    ["created_at", "创建时间"], ["updated_at", "更新时间"],
  ]) {
    const item = el("li");
    item.append(el("span", "", label), el("strong", "", String(snapshot[key] ?? "—")));
    list.append(item);
  }
  content.append(list);
  const actions = el("div", "dialog-actions");
  const dialog = openDialog("快照详情", "失败原因与 checksum 用于定位导入问题。", content);
  actions.append(makeButton("关闭", "button", () => dialog.close()));
  content.append(actions);
}

function openImportDialog() {
  const form = el("form", "dialog-form");
  const grid = el("div", "form-grid");
  const providerSelect = selectInput("provider_id", state.providers.map((p) => [p.id, `${p.id} · ${p.namespace}`]), state.providers[0]?.id);
  const namespaceSelect = selectInput("namespace", state.namespaces.map((ns) => [ns.id, ns.id]), state.providers[0]?.namespace ?? state.namespaces[0]?.id);
  // A provider can only import into its own namespace.
  providerSelect.addEventListener("change", () => {
    const provider = state.providers.find((item) => item.id === providerSelect.value);
    if (provider) namespaceSelect.value = provider.namespace;
  });
  const sourceInput = textInput("source_version", `feed-${new Date().toISOString().slice(0, 10)}`);
  const rowsInput = textInput("expected_rows", "", "成员行数");
  rowsInput.type = "number";
  const checksumInput = textInput("expected_checksum", "", "可选：sha256 hex");
  const payloadArea = el("textarea");
  payloadArea.rows = 8;
  payloadArea.placeholder = '[{"subject_token":"<64 hex>","subject_key_version":1,"segment_ids":["seg-x"]}]';
  payloadArea.value = "";
  const payloadField = el("div", "form-field full");
  payloadField.append(el("label", "", "成员行（JSON 数组）"), payloadArea);
  grid.append(field("Provider", providerSelect), field("Namespace", namespaceSelect), field("来源版本", sourceInput),
    field("预期行数", rowsInput), field("预期 checksum", checksumInput), payloadField);
  const error = formError();
  const actions = el("div", "dialog-actions");
  actions.append(makeButton("取消", "button", () => dialog.close()));
  const submit = makeButton("导入并校验", "button button-primary", () => {});
  submit.type = "submit";
  actions.append(submit);
  form.append(grid, error, actions);
  const dialog = openDialog("新建快照导入",
    "一次导入 = 登记 → 批量写入 → 校验；原始身份（邮箱/手机号/设备 ID）会被服务端拒绝。", form);

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    submit.disabled = true;
    submit.textContent = "导入中…";
    try {
      let rows;
      try {
        rows = JSON.parse(payloadArea.value || "[]");
      } catch {
        throw new Error("成员行不是合法 JSON");
      }
      if (!Array.isArray(rows) || rows.length === 0) throw new Error("请至少提供一行成员数据");
      const expected = Number(rowsInput.value || rows.length);
      const job = await apiFetch("/internal/v1/snapshots", {
        method: "POST",
        body: JSON.stringify({
          provider_id: providerSelect.value,
          namespace: namespaceSelect.value,
          schema_version: 1,
          source_version: sourceInput.value.trim(),
          expected_rows: expected,
          ...(checksumInput.value.trim() ? { expected_checksum: checksumInput.value.trim() } : {}),
        }),
      });
      await apiFetch(`/internal/v1/snapshots/${job.snapshot_id}/rows`, { method: "POST", body: JSON.stringify({ rows }) });
      try {
        const verified = await apiFetch(`/internal/v1/snapshots/${job.snapshot_id}/verify`, { method: "POST" });
        showToast(verified.status === "verified" ? "导入完成并通过校验，可发布" : `校验未通过：${verified.failure_reason}`);
      } catch (verifyError) {
        showToast(`导入已写入但校验失败：${verifyError.message}`);
      }
      dialog.close();
      await loadAndRender(true);
    } catch (importError) {
      submit.disabled = false;
      submit.textContent = "导入并校验";
      showFormError(error, importError.message);
    }
  });
}

// -- revocations -------------------------------------------------------------

function renderRevocations(main) {
  main.append(pageHeader("revocations", "＋ 登记撤回", () => openRevokeDialog()));
  main.append(note("门禁语义", "撤回先原子写入 deny gate（阻断新 admission 并等待在途 lookup），再追加 ClickHouse tombstone；状态不确定时解析一律 503。控制台只接受伪名 token，不接收原始身份。", "⚑"));

  main.append(tableCard("撤回状态", "physical_delete_status 由保留策略推进", ["撤回 ID", "在线状态", "物理删除", "Gate epoch", "在途数", "更新时间", ""],
    state.revocations.map((revocation) => [
      nodeCell(el("code", "", short(revocation.revocation_id, 16))),
      nodeCell(statusPill(revocation.online_status)),
      nodeCell(statusPill(revocation.physical_delete_status)),
      cell(String(revocation.gate_epoch)),
      cell(String(revocation.drained_lookup_count)),
      cell(fmtTime(revocation.updated_at)),
      nodeCell(makeButton("⋯", "icon-button", () => showRevocationDetails(revocation), `查看 ${revocation.revocation_id}`)),
    ])));
}

function showRevocationDetails(revocation) {
  const content = el("div", "dialog-form");
  const list = el("ul", "detail-list");
  for (const [key, label] of [
    ["revocation_id", "撤回 ID"], ["status_version", "状态版本"], ["online_status", "在线状态"],
    ["physical_delete_status", "物理删除"], ["gate_epoch", "Gate epoch"],
    ["drained_lookup_count", "在途数"], ["covered_key_versions", "覆盖 key version"], ["updated_at", "更新时间"],
  ]) {
    const item = el("li");
    const value = Array.isArray(revocation[key]) ? revocation[key].join(", ") : revocation[key];
    item.append(el("span", "", label), el("strong", "", String(value ?? "—")));
    list.append(item);
  }
  content.append(list);
  const actions = el("div", "dialog-actions");
  const dialog = openDialog("撤回详情", "状态由 DMP 追加写入，控制台只读。", content);
  actions.append(makeButton("关闭", "button", () => dialog.close()));
  content.append(actions);
}

function openRevokeDialog() {
  const form = el("form", "dialog-form");
  const grid = el("div", "form-grid");
  const namespaceSelect = selectInput("namespace", state.namespaces.map((ns) => [ns.id, ns.id]), state.namespaces[0]?.id);
  const tokenInput = textInput("subject_token", "", "64 位小写 hex 伪名 token");
  const keyVersionInput = textInput("key_version", "1");
  keyVersionInput.type = "number";
  const reasonSelect = selectInput("reason_code", [["user_request", "user_request"], ["consent_withdrawn", "consent_withdrawn"]], "user_request");
  grid.append(field("Namespace", namespaceSelect), field("Subject token", tokenInput),
    field("Key version", keyVersionInput), field("原因", reasonSelect));
  const error = formError();
  const actions = el("div", "dialog-actions");
  actions.append(makeButton("取消", "button", () => dialog.close()));
  const submit = makeButton("登记撤回", "button button-danger", () => {});
  submit.type = "submit";
  actions.append(submit);
  form.append(grid, error, actions);
  const dialog = openDialog("登记撤回",
    "只接受伪名 token（64 位小写 hex）；原始邮箱、手机号或设备 ID 会被拒绝，也不会被发送。", form);

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    submit.disabled = true;
    try {
      const token = tokenInput.value.trim().toLowerCase();
      if (!/^[0-9a-f]{64}$/.test(token)) throw new Error("subject token 必须是 64 位小写 hex 伪名标识");
      const result = await apiFetch("/internal/v1/identity:revoke", {
        method: "POST",
        body: JSON.stringify({
          namespace: namespaceSelect.value,
          subject_tokens: [{ token, key_version: Number(keyVersionInput.value || 1) }],
          reason_code: reasonSelect.value,
        }),
      });
      dialog.close();
      showToast(`撤回已受理：${result.status}（在线状态 ${result.online_status}）`);
      await loadAndRender(true);
    } catch (revokeError) {
      submit.disabled = false;
      showFormError(error, revokeError.message);
    }
  });
}

// -- retention ---------------------------------------------------------------

function renderRetention(main) {
  main.append(pageHeader("retention", "立即执行 sweep", runSweep));
  const policy = state.retention;
  if (!policy) {
    main.append(note("策略不可用", "DMP 未配置 retention worker。", "⌛"));
    return;
  }
  const stats = el("section", "stats-grid");
  stats.append(
    statCard("Membership 保留", `${Math.round(policy.membership_retention_hours / 24)} 天`, `上限 ${policy.membership_retention_ceiling}`, "⌛"),
    statCard("事件保留", `${Math.round(policy.event_retention_hours / 24)} 天`, `上限 ${policy.event_retention_ceiling}`, "◫"),
    statCard("物理删除 SLA", `${Math.round(policy.physical_delete_sla_hours / 24)} 天`, `上限 ${policy.physical_delete_sla_ceiling}`, "⚑"),
    statCard("物理删除宽限", `${policy.physical_delete_grace_hours} 小时`, "撤回后进入物理删除队列的等待", "◷"),
  );
  main.append(stats);
  main.append(note("删除边界", "快照删除按 tenant + snapshot_id 精确执行；同月分区中的其他快照不受影响，当前服务快照永不删除。撤回 token 的物理删除会清空 membership 与事件行，但保留 tombstone。", "⌛"));

  if (state.lastSweep) {
    const sweep = state.lastSweep;
    main.append(tableCard("最近一次 sweep", fmtTime(sweep.finished_at), ["过期快照", "删除行数", "物理删除主体", "待处理", "失败"], [[
      cell(String(sweep.expired_snapshots?.length ?? 0)),
      cell(String(sweep.deleted_rows ?? 0)),
      cell(String(sweep.purged_subjects ?? 0)),
      cell(String(sweep.pending_purges ?? 0)),
      cell(String(sweep.failures?.length ?? 0)),
    ]]));
  }
}

async function runSweep() {
  try {
    state.lastSweep = await apiFetch("/internal/v1/retention:run", { method: "POST" });
    showToast(`sweep 完成：删除快照 ${state.lastSweep.expired_snapshots.length} 个，物理删除 ${state.lastSweep.purged_subjects} 个主体`);
    await loadAndRender(true);
  } catch (error) {
    handleError(error);
  }
}

// -- data + render -----------------------------------------------------------

async function refreshAll() {
  const [providers, namespaces, segments, snapshots, publications, revocations, retention] = await Promise.all([
    apiFetch("/admin/v1/providers"),
    apiFetch("/admin/v1/namespaces"),
    apiFetch("/admin/v1/segments"),
    apiFetch("/internal/v1/snapshots"),
    apiFetch("/internal/v1/publications"),
    apiFetch("/internal/v1/identity-revocations"),
    apiFetch("/internal/v1/retention:policy"),
  ]);
  state.providers = providers ?? [];
  state.namespaces = namespaces ?? [];
  state.segments = segments ?? [];
  state.snapshots = snapshots ?? [];
  state.publications = publications ?? [];
  state.revocations = revocations ?? [];
  state.retention = retention;
  state.publishedCount = state.publications.length;
  state.pendingRevocations = state.revocations.filter((r) => r.physical_delete_status === "pending").length;
  state.connected = true;
  state.error = "";
}

function renderPage() {
  const main = document.querySelector("#main-content");
  if (!main) return;
  main.replaceChildren();
  document.title = `${pages[currentPage].title} · DMP · Open Ads`;
  if (!state.connected) {
    main.append(pageHeader(currentPage));
    main.append(note("DMP API 不可达", state.error || "无法连接 console 代理；请确认 DMP 与代理配置。", "⚠"));
    main.append(makeButton("重试", "button", () => loadAndRender(true)));
    return;
  }
  if (currentPage === "overview") return renderOverview(main);
  if (currentPage === "providers" || currentPage === "segments") return renderDefinitions(main, currentPage);
  if (currentPage === "snapshots") return renderSnapshots(main);
  if (currentPage === "revocations") return renderRevocations(main);
  return renderRetention(main);
}

async function loadAndRender(force = false) {
  if (force || !state.connected) {
    try {
      await refreshAll();
    } catch (error) {
      state.connected = false;
      state.error = error?.message || "连接失败";
    }
  }
  renderShell();
  renderPage();
}

window.addEventListener("hashchange", () => {
  const target = location.hash.slice(1);
  currentPage = pages[target] ? target : "overview";
  loadAndRender(false);
});

loadAndRender();
