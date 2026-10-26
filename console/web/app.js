const form = document.querySelector("#request-form");
const modeButtons = [...document.querySelectorAll("[data-mode]")];
const endpointField = document.querySelector("#endpoint-field");
const endpointInput = document.querySelector("#endpoint");
const placementSelect = document.querySelector("#placement");
const widthInput = document.querySelector("#width");
const heightInput = document.querySelector("#height");
const sendButton = document.querySelector("#send-button");
const resultBadge = document.querySelector("#result-badge");
const emptyState = document.querySelector("#empty-state");
const loadingState = document.querySelector("#loading-state");
const adPreview = document.querySelector("#ad-preview");
const noFillState = document.querySelector("#no-fill-state");
const errorState = document.querySelector("#error-state");
const diagnosticsEmpty = document.querySelector("#diagnostics-empty");
const diagnosticsContent = document.querySelector("#diagnostics-content");
const copyButton = document.querySelector("#copy-button");
const toast = document.querySelector("#toast");
const knownViews = [emptyState, loadingState, adPreview, noFillState, errorState];
let mode = "simulate";
let lastRequestSummary;
let activeController;
let toastTimer;

function setMode(nextMode) {
  mode = nextMode;
  for (const button of modeButtons) {
    const selected = button.dataset.mode === mode;
    button.classList.toggle("selected", selected);
    button.setAttribute("aria-pressed", String(selected));
  }
  endpointField.classList.toggle("hidden", mode !== "live");
  document.querySelector("#scenario").disabled = mode !== "simulate";
}

function setView(view, statusText, badgeClass) {
  for (const element of knownViews) element.classList.add("hidden");
  view.classList.remove("hidden");
  resultBadge.className = `result-badge ${badgeClass}`;
  resultBadge.innerHTML = `<span></span>${statusText}`;
}

function newRequestId() {
  if (crypto.randomUUID) return crypto.randomUUID().replaceAll("-", "");
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return [...bytes].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

function selectedPlacement() {
  return placementSelect.selectedOptions[0];
}

function updateDimensionsFromPlacement() {
  const option = selectedPlacement();
  widthInput.value = option.dataset.width;
  heightInput.value = option.dataset.height;
  document.querySelector("#size-label").textContent = `${widthInput.value} × ${heightInput.value} px`;
}

function readForm() {
  const width = Number(widthInput.value);
  const height = Number(heightInput.value);
  const pageOrigin = document.querySelector("#origin").value.trim();
  const path = document.querySelector("#path").value.trim();
  let origin;

  if (!Number.isInteger(width) || width < 1 || width > 10000 ||
      !Number.isInteger(height) || height < 1 || height > 10000) {
    throw new Error("广告位宽高必须是 1 到 10,000 之间的整数。");
  }
  try {
    origin = new URL(pageOrigin);
  } catch {
    throw new Error("请输入有效的页面 Origin。");
  }
  if (!["https:", "http:"].includes(origin.protocol) || origin.origin !== pageOrigin) {
    throw new Error("页面 Origin 只能包含协议和主机，且须使用 HTTP 或 HTTPS。");
  }
  if (!path.startsWith("/") || path.includes("?") || path.includes("#")) {
    throw new Error("页面路径必须以 / 开头，且不能包含 query 或 fragment。");
  }

  return {
    request_id: newRequestId(),
    placement_id: placementSelect.value,
    slot: { id: selectedPlacement().dataset.slot, width, height },
    page: { origin: origin.origin, path }
  };
}

function makeMockResult(request, scenario) {
  const duration = scenario === "error" ? 61 : scenario === "no_fill" ? 48 : 67;
  const stages = scenario === "error"
    ? [["验证请求", 3], ["查询配置快照", 7], ["DSP 竞价", 44], ["响应处理", 7]]
    : [["验证请求", 3], ["查询配置快照", 6], ["DSP 并发竞价", scenario === "no_fill" ? 30 : 43], ["响应处理", scenario === "no_fill" ? 9 : 15]];
  const status = scenario === "error" ? "error" : scenario;
  const response = scenario === "filled"
    ? {
        request_id: request.request_id,
        status: "filled",
        ad: { creative_id: "creative_demo_spring", format: "image", expires_at: new Date(Date.now() + 30000).toISOString() },
        diagnostics: { auction_id: newRequestId(), outcome: "bid", duration_ms: duration }
      }
    : scenario === "no_fill"
      ? { request_id: request.request_id, status: "no_fill", diagnostics: { outcome: "no_bid", duration_ms: duration } }
      : { request_id: request.request_id, status: "error", code: "dependency_unavailable", message: "Sandbox 服务暂时不可用。" };

  return { status, httpStatus: scenario === "error" ? 503 : 200, duration, stages, response };
}

function responseSummary(response) {
  const summary = structuredClone(response);
  if (summary.ad) {
    delete summary.ad.render_url;
    delete summary.ad.click_url;
    delete summary.ad.event_token;
    summary.ad.render = "可用（短时令牌已隐藏）";
  }
  return summary;
}

async function requestMock(payload) {
  const scenario = document.querySelector("#scenario").value;
  await new Promise((resolve) => window.setTimeout(resolve, 650));
  const result = makeMockResult(payload, scenario);
  if (scenario === "error") throw Object.assign(new Error(result.response.message), { result });
  return result;
}

function validateEndpoint(value) {
  let endpoint;
  try {
    endpoint = new URL(value);
  } catch {
    throw new Error("请输入有效的 SSP Sandbox 地址。");
  }
  const localHttp = endpoint.protocol === "http:" &&
    ["localhost", "127.0.0.1", "[::1]"].includes(endpoint.hostname);
  if (endpoint.protocol !== "https:" && !localHttp) {
    throw new Error("API 地址必须使用 HTTPS（本机 localhost 可使用 HTTP）。");
  }
  if (endpoint.username || endpoint.password) {
    throw new Error("API 地址不能包含用户名或密码。");
  }
  // Ensure the endpoint ends with a single '/sdk/v1/ad-requests' segment.
  const basePath = endpoint.pathname.replace(/\/+$/, "");
  if (!basePath.endsWith('/sdk/v1/ad-requests')) {
    endpoint.pathname = `${basePath}/sdk/v1/ad-requests`;
  } else {
    endpoint.pathname = basePath;
  }
  endpoint.search = "";
  endpoint.hash = "";
  return endpoint;
}

async function requestLive(payload) {
  const endpoint = validateEndpoint(endpointInput.value.trim());
  activeController = new AbortController();
  const timeout = window.setTimeout(() => activeController.abort(), 1000);
  const startedAt = performance.now();
  try {
    const response = await fetch(endpoint, {
      method: "POST",
      mode: "cors",
      credentials: "omit",
      cache: "no-store",
      signal: activeController.signal,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        request_id: payload.request_id,
        placement_id: payload.placement_id,
        slot: payload.slot,
        page: payload.page,
        consent: { source: "test-fixture", value: document.querySelector("#consent").checked ? "sandbox-consent-demo-01" : "" }
      })
    });
    const body = await response.json();
    const duration = Math.round(performance.now() - startedAt);
    const result = {
      status: response.ok ? body.status : "error",
      httpStatus: response.status,
      duration,
      stages: Array.isArray(body.diagnostics?.stages) ? body.diagnostics.stages : [["SSP Client API", duration]],
      response: body
    };
    if (!response.ok || body.status === "error") {
      throw Object.assign(new Error(body.message || "Sandbox API 返回错误。"), { result });
    }
    if (!["filled", "no_fill"].includes(body.status)) {
      throw Object.assign(new Error("Sandbox API 返回了不支持的响应状态。"), { result });
    }
    if (body.status === "filled") {
      let renderURL;
      try {
        renderURL = new URL(body.ad?.render_url);
      } catch {
        throw Object.assign(new Error("Sandbox API 的广告响应缺少有效的 render_url。"), { result });
      }
      const isLoopback = (url) =>
        (url.hostname === "127.0.0.1" || url.hostname === "localhost") && url.protocol === "http:";
      if (!(renderURL.protocol === "https:" || isLoopback(renderURL)) || renderURL.origin !== endpoint.origin ||
          renderURL.username || renderURL.password) {
        throw Object.assign(new Error("广告 render_url 必须使用配置的 SSP 地址同源 HTTPS（localhost 可 HTTP），且不能包含用户名或密码。"), { result });
      }
    }
    return result;
  } catch (error) {
    if (error.name === "AbortError") throw new Error("请求超过 1 秒超时，请检查 Sandbox 服务。");
    if (error instanceof TypeError) throw new Error("无法连接 SSP Sandbox，请检查地址、服务状态和 CORS 配置。");
    throw error;
  } finally {
    window.clearTimeout(timeout);
    activeController = undefined;
  }
}

function renderDiagnostics(payload, result) {
  const response = responseSummary(result.response);
  lastRequestSummary = {
    request_id: payload.request_id,
    outcome: result.status,
    http_status: result.httpStatus,
    duration_ms: result.duration,
    stages: result.stages,
    response
  };
  document.querySelector("#request-id").textContent = payload.request_id;
  document.querySelector("#outcome-value").textContent = result.status === "filled" ? "filled" : result.status === "no_fill" ? "no_fill" : "error";
  document.querySelector("#outcome-value").className = result.status === "filled" ? "success" : result.status === "no_fill" ? "warning" : "failure";
  document.querySelector("#duration-value").textContent = `${result.duration} ms`;
  document.querySelector("#http-value").textContent = String(result.httpStatus);
  document.querySelector("#http-value").className = result.httpStatus < 400 ? "success" : "failure";
  document.querySelector("#timeline").replaceChildren(...result.stages.map(([label, duration]) => {
    const item = document.createElement("li");
    const stage = document.createElement("span");
    const elapsed = document.createElement("code");
    stage.textContent = label;
    elapsed.textContent = `${duration} ms`;
    item.append(stage, elapsed);
    return item;
  }));
  document.querySelector("#response-summary").textContent = JSON.stringify(response, null, 2);
  diagnosticsEmpty.classList.add("hidden");
  diagnosticsContent.classList.remove("hidden");
  copyButton.disabled = false;
}

function showResult(payload, result) {
  renderDiagnostics(payload, result);
  document.querySelector("#size-label").textContent = `${payload.slot.width} × ${payload.slot.height} px`;
  if (result.status === "filled") {
    const mockFrame = document.querySelector("#ad-frame");
    const renderFrame = document.querySelector("#render-frame");
    const liveRender = mode === "live" && result.response.ad?.render_url;
    mockFrame.classList.toggle("hidden", Boolean(liveRender));
    renderFrame.classList.toggle("hidden", !liveRender);
    if (liveRender) {
      renderFrame.src = new URL(result.response.ad.render_url).href;
      document.querySelector("#preview-caption-text").textContent = "Sandbox creative · 隔离 iframe";
    } else {
      renderFrame.removeAttribute("src");
      document.querySelector("#preview-caption-text").textContent = "creative_demo_spring · 模拟素材";
      mockFrame.style.aspectRatio = `${payload.slot.width} / ${payload.slot.height}`;
      mockFrame.style.maxWidth = `${payload.slot.width}px`;
      mockFrame.style.minHeight = `${Math.min(payload.slot.height, 250)}px`;
    }
    setView(adPreview, "广告已填充", "filled");
  } else if (result.status === "no_fill") {
    setView(noFillState, "无广告", "no-fill");
  } else {
    document.querySelector("#error-title").textContent = "请求未能完成";
    document.querySelector("#error-message").textContent = "请检查配置和 Sandbox 服务状态后重试。";
    setView(errorState, "请求错误", "error");
  }
}

function showError(message) {
  document.querySelector("#error-title").textContent = "配置无效";
  document.querySelector("#error-message").textContent = message;
  setView(errorState, "配置错误", "error");
}

async function submitRequest() {
  if (activeController) activeController.abort();
  let payload;
  try {
    payload = readForm();
  } catch (error) {
    showError(error.message);
    return;
  }

  sendButton.disabled = true;
  setView(loadingState, "请求中", "loading");
  document.querySelector("#size-label").textContent = `${payload.slot.width} × ${payload.slot.height} px`;
  try {
    const result = mode === "simulate" ? await requestMock(payload) : await requestLive(payload);
    showResult(payload, result);
  } catch (error) {
    const result = error.result;
    if (result) {
      showResult(payload, { ...result, status: "error" });
    } else {
      showError(error.message);
      diagnosticsEmpty.classList.remove("hidden");
      diagnosticsContent.classList.add("hidden");
      copyButton.disabled = true;
      lastRequestSummary = undefined;
    }
  } finally {
    sendButton.disabled = false;
  }
}

function showToast(message) {
  toast.textContent = message;
  toast.classList.remove("hidden");
  window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => toast.classList.add("hidden"), 1800);
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    showToast("已复制到剪贴板");
  } catch {
    showToast("复制失败，请检查浏览器剪贴板权限");
  }
}

modeButtons.forEach((button) => button.addEventListener("click", () => setMode(button.dataset.mode)));
placementSelect.addEventListener("change", updateDimensionsFromPlacement);
widthInput.addEventListener("input", () => {
  document.querySelector("#size-label").textContent = `${widthInput.value || "—"} × ${heightInput.value || "—"} px`;
});
heightInput.addEventListener("input", () => {
  document.querySelector("#size-label").textContent = `${widthInput.value || "—"} × ${heightInput.value || "—"} px`;
});
form.addEventListener("submit", (event) => {
  event.preventDefault();
  submitRequest();
});
document.querySelector("#retry-button").addEventListener("click", submitRequest);
document.querySelector("#error-retry-button").addEventListener("click", submitRequest);
document.querySelector("#copy-button").addEventListener("click", () => {
  if (lastRequestSummary) copyText(JSON.stringify(lastRequestSummary, null, 2));
});
document.querySelector("#copy-id-button").addEventListener("click", () => {
  if (lastRequestSummary) copyText(lastRequestSummary.request_id);
});
document.querySelector("#open-render-button").addEventListener("click", () => {
  const renderFrame = document.querySelector("#render-frame");
  if (mode === "live" && renderFrame.src) {
    renderFrame.classList.remove("hidden");
    document.querySelector("#ad-frame").classList.add("hidden");
    return;
  }
  showToast("模拟素材已显示在隔离预览中");
});
document.querySelector("#reset-button").addEventListener("click", () => {
  form.reset();
  setMode("simulate");
  updateDimensionsFromPlacement();
  lastRequestSummary = undefined;
  diagnosticsEmpty.classList.remove("hidden");
  diagnosticsContent.classList.add("hidden");
  copyButton.disabled = true;
  document.querySelector("#ad-frame").removeAttribute("style");
  document.querySelector("#ad-frame").classList.remove("hidden");
  document.querySelector("#render-frame").classList.add("hidden");
  document.querySelector("#render-frame").removeAttribute("src");
  setView(emptyState, "等待请求", "idle");
});
document.addEventListener("keydown", (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key === "Enter" && !sendButton.disabled) {
    event.preventDefault();
    submitRequest();
  }
});

updateDimensionsFromPlacement();
