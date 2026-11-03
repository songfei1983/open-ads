// Open Ads sandbox JS SDK (ES module). It only talks to the SSP Client API and
// never retries an ad request implicitly: a retry is a new auction.
const DEFAULT_TIMEOUT_MS = 1500;

export class OpenAdsSDKError extends Error {
  constructor(code, message, status) {
    super(message || code);
    this.name = "OpenAdsSDKError";
    this.code = code || "sdk_error";
    this.status = status || 0;
  }
}

export class OpenAdsSDK {
  constructor({ baseUrl, placementId, timeoutMs } = {}) {
    if (!baseUrl) throw new OpenAdsSDKError("invalid_config", "baseUrl is required");
    if (!placementId) throw new OpenAdsSDKError("invalid_config", "placementId is required");
    this.baseUrl = String(baseUrl).replace(/\/+$/, "");
    this.placementId = placementId;
    this.timeoutMs = timeoutMs || DEFAULT_TIMEOUT_MS;
    this.active = null;
  }

  async requestAd({ slot = {}, consent = null, page = null, signal } = {}) {
    if (typeof slot.id !== "string" || !slot.id) {
      throw new OpenAdsSDKError("invalid_request", "slot.id is required");
    }
    if (!Number.isInteger(slot.width) || !Number.isInteger(slot.height) || slot.width <= 0 || slot.height <= 0) {
      throw new OpenAdsSDKError("invalid_request", "slot dimensions are required");
    }
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), this.timeoutMs);
    const abort = () => controller.abort();
    if (signal) {
      if (signal.aborted) controller.abort();
      else signal.addEventListener("abort", abort, { once: true });
    }
    const payload = {
      placement_id: this.placementId,
      slot: { id: slot.id, width: slot.width, height: slot.height },
      page: page || { origin: location.origin, path: location.pathname },
      consent: consent || { source: "sdk", value: null },
    };
    try {
      const response = await fetch(this.baseUrl + "/sdk/v1/ad-requests", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
        credentials: "omit",
        cache: "no-store",
        signal: controller.signal,
      });
      const body = await response.json().catch(() => ({}));
      if (!response.ok) {
        throw new OpenAdsSDKError(body.code || "request_failed", body.message, response.status);
      }
      return body;
    } catch (error) {
      if (error && error.name === "AbortError") {
        throw new OpenAdsSDKError("timeout", "ad request was aborted", 0);
      }
      throw error instanceof OpenAdsSDKError
        ? error
        : new OpenAdsSDKError("network_error", String(error && error.message ? error.message : error), 0);
    } finally {
      clearTimeout(timeout);
      if (signal) signal.removeEventListener("abort", abort);
    }
  }

  render(container, ad, { onEvent } = {}) {
    if (!container || !ad || !ad.render_url) {
      throw new OpenAdsSDKError("invalid_render", "a filled ad response is required");
    }
    this.destroy();
    const iframe = document.createElement("iframe");
    iframe.src = ad.render_url;
    iframe.title = "Advertisement";
    iframe.setAttribute("sandbox", "allow-scripts allow-popups allow-popups-to-escape-sandbox");
    iframe.setAttribute("referrerpolicy", "no-referrer");
    iframe.setAttribute("loading", "eager");
    iframe.style.border = "0";
    iframe.style.width = "100%";
    iframe.style.height = "100%";
    container.replaceChildren(iframe);
    this.active = iframe;
    if (typeof onEvent === "function") onEvent({ type: "rendered", creativeId: ad.creative_id });
    return iframe;
  }

  destroy() {
    if (this.active && this.active.parentNode) this.active.parentNode.removeChild(this.active);
    this.active = null;
  }
}

export default OpenAdsSDK;
