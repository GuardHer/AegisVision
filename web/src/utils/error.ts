import { ElMessage } from "element-plus";

const GENERIC_AXIOS_STATUS_RE = /^Request failed with status code \d{3}$/i;

export type ErrorTranslateFn = (key: string) => string;

let globalErrorTranslator: ErrorTranslateFn | null = null;

/** Registers vue-i18n translator from main.ts so all error helpers localize dynamically. */
export function setErrorTranslator(fn: ErrorTranslateFn | null): void {
  globalErrorTranslator = fn;
}

function tr(key: string, defaultText: string, t?: ErrorTranslateFn): string {
  const fn = t ?? globalErrorTranslator;
  if (!fn) return defaultText;
  try {
    const res = fn(key);
    if (res && res !== key) return res;
  } catch {
    // Fall back to defaultText
  }
  return defaultText;
}

const HTTP_STATUS_KEY_MAP: Record<number, { key: string; fallback: string }> = {
  400: { key: "aegis.errors.http400", fallback: "Bad request (HTTP 400)" },
  401: { key: "aegis.errors.http401", fallback: "Authentication required or session expired (HTTP 401)" },
  403: { key: "aegis.errors.http403", fallback: "Permission denied (HTTP 403)" },
  404: { key: "aegis.errors.http404", fallback: "Requested resource not found (HTTP 404)" },
  409: { key: "aegis.errors.http409", fallback: "Resource conflict or already exists (HTTP 409)" },
  422: { key: "aegis.errors.http422", fallback: "Validation failed (HTTP 422)" },
  429: { key: "aegis.errors.http429", fallback: "Too many requests, please retry later (HTTP 429)" },
  500: { key: "aegis.errors.http500", fallback: "Internal server error (HTTP 500)" },
  502: { key: "aegis.errors.http502", fallback: "Service gateway unavailable (HTTP 502)" },
  503: { key: "aegis.errors.http503", fallback: "Service temporarily unavailable (HTTP 503)" },
  504: { key: "aegis.errors.http504", fallback: "Gateway timeout (HTTP 504)" }
};

/** Translates well-known backend English error strings into current UI locale when translator is active. */
export function localizeBackendMessage(rawMsg: string, t?: ErrorTranslateFn): string {
  const trimmed = rawMsg.trim();
  if (!trimmed) return trimmed;
  const fn = t ?? globalErrorTranslator;
  if (!fn) return trimmed;

  const lower = trimmed.toLowerCase();
  const patterns: Array<[string, string]> = [
    ["invalid username or password", "login.invalidCredentials"],
    ["user account is disabled", "login.accountDisabled"],
    ["username and password are required", "login.credentialsRequired"],
    ["resource already exists", "aegis.errors.resourceExists"],
    ["resource not found", "aegis.errors.resourceNotFound"],
    ["referenced resource does not exist or is still in use", "aegis.errors.resourceInUse"],
    ["permission denied", "aegis.errors.permissionDenied"],
    ["forbidden", "aegis.errors.permissionDenied"],
    ["constraint validation failed", "aegis.errors.constraintFailed"],
    ["internal server error", "aegis.errors.internalServerError"],
    ["cannot delete zone", "aegis.errors.zoneNotEmpty"],
    ["referenced by rules", "aegis.errors.scheduleReferenced"]
  ];
  for (const [needle, i18nKey] of patterns) {
    if (lower.includes(needle)) {
      return tr(i18nKey, trimmed, fn);
    }
  }
  return trimmed;
}

/** Returns true for user-cancelled confirmations or aborted HTTP requests. */
export function isCancelError(err: unknown): boolean {
  if (err === "cancel" || err === "close") return true;
  if (!err || typeof err !== "object") return false;
  const anyErr = err as Record<string, any>;
  return Boolean(anyErr.isCancelRequest) || anyErr.code === "ERR_CANCELED";
}

function isHtmlPayload(text: string): boolean {
  const head = text.trim().slice(0, 64).toLowerCase();
  return head.startsWith("<!doctype") || head.startsWith("<html");
}

/** Extracts a concise, localized user-facing error message from an Axios/API/runtime error. */
export function extractErrorMessage(
  err: unknown,
  fallback?: string
): string {
  const resolvedFallback =
    fallback || tr("aegis.errors.operationFailed", "Operation failed");

  if (typeof err === "string") {
    const trimmed = err.trim();
    return trimmed ? localizeBackendMessage(trimmed) : resolvedFallback;
  }
  if (!err || typeof err !== "object") {
    return resolvedFallback;
  }

  const anyErr = err as Record<string, any>;
  const data = anyErr.response?.data;

  if (data && typeof data === "object" && !(data instanceof Blob)) {
    const backendMsg =
      typeof data.message === "string"
        ? data.message.trim()
        : typeof data.error === "string"
          ? data.error.trim()
          : "";
    if (backendMsg) return localizeBackendMessage(backendMsg);
  } else if (typeof data === "string") {
    const trimmed = data.trim();
    if (trimmed && !isHtmlPayload(trimmed)) {
      const clipped = trimmed.length > 240 ? `${trimmed.slice(0, 240)}...` : trimmed;
      return localizeBackendMessage(clipped);
    }
  }

  const code = typeof anyErr.code === "string" ? anyErr.code : "";
  if (code === "ECONNABORTED" || code === "ETIMEDOUT") {
    return tr("aegis.errors.timeoutError", "Request timeout, please check network or retry");
  }
  if (code === "ERR_NETWORK" || anyErr.message === "Network Error") {
    return tr(
      "aegis.errors.networkError",
      "Unable to reach server (network error), please check backend service"
    );
  }

  const status = Number(anyErr.response?.status);
  if (Number.isFinite(status) && HTTP_STATUS_KEY_MAP[status]) {
    const rawMsg = typeof anyErr.message === "string" ? anyErr.message.trim() : "";
    if (!rawMsg || GENERIC_AXIOS_STATUS_RE.test(rawMsg)) {
      const entry = HTTP_STATUS_KEY_MAP[status];
      return tr(entry.key, entry.fallback);
    }
  }

  if (typeof anyErr.message === "string") {
    const trimmed = anyErr.message.trim();
    if (trimmed && !GENERIC_AXIOS_STATUS_RE.test(trimmed)) {
      return localizeBackendMessage(trimmed);
    }
  }

  return resolvedFallback;
}

/** Async version of extractErrorMessage that also decodes JSON error envelopes inside Blob responses. */
export async function extractAsyncErrorMessage(
  err: unknown,
  fallback?: string
): Promise<string> {
  if (err && typeof err === "object") {
    const anyErr = err as Record<string, any>;
    const data = anyErr.response?.data;
    if (typeof Blob !== "undefined" && data instanceof Blob) {
      try {
        const text = await data.text();
        const trimmed = text.trim();
        if (trimmed) {
          try {
            const parsed = JSON.parse(trimmed);
            if (typeof parsed?.message === "string" && parsed.message.trim()) {
              return localizeBackendMessage(parsed.message.trim());
            }
          } catch {
            if (!isHtmlPayload(trimmed)) {
              const clipped = trimmed.length > 240 ? `${trimmed.slice(0, 240)}...` : trimmed;
              return localizeBackendMessage(clipped);
            }
          }
        }
      } catch {
        // Fall through to synchronous extraction
      }
    }
  }
  return extractErrorMessage(err, fallback);
}

/** Shows a grouped, closable Element Plus error toast unless the error was a user cancel. */
export function showError(err: unknown, fallback?: string): string {
  if (isCancelError(err)) return "";
  const msg = extractErrorMessage(err, fallback);
  ElMessage.error({
    message: msg,
    grouping: true,
    showClose: true
  });
  return msg;
}

/** Async variant for endpoints returning Blob payloads (CSV/ZIP exports). */
export async function showAsyncError(
  err: unknown,
  fallback?: string
): Promise<string> {
  if (isCancelError(err)) return "";
  const msg = await extractAsyncErrorMessage(err, fallback);
  ElMessage.error({
    message: msg,
    grouping: true,
    showClose: true
  });
  return msg;
}

/** Shows a grouped Element Plus success toast. */
export function showSuccess(message: string): void {
  ElMessage.success({
    message,
    grouping: true
  });
}

/** Maps common backend auth errors to localized i18n text while keeping unknown server messages intact. */
export function formatAuthError(
  err: unknown,
  t: (key: string) => string
): string {
  if (err && typeof err === "object") {
    const anyErr = err as Record<string, any>;
    const code = typeof anyErr.code === "string" ? anyErr.code : "";
    if (code === "ECONNABORTED" || code === "ETIMEDOUT") {
      return t("login.timeoutError");
    }
    if (code === "ERR_NETWORK" || anyErr.message === "Network Error") {
      return t("login.networkError");
    }
  }

  const raw = extractErrorMessage(err, t("login.pureLoginFail"));
  const normalized = raw.toLowerCase();
  if (normalized.includes("invalid username or password")) {
    return t("login.invalidCredentials");
  }
  if (normalized.includes("user account is disabled")) {
    return t("login.accountDisabled");
  }
  if (normalized.includes("username and password are required")) {
    return t("login.credentialsRequired");
  }
  if (normalized.includes("network error")) {
    return t("login.networkError");
  }
  if (normalized.includes("timeout")) {
    return t("login.timeoutError");
  }
  return localizeBackendMessage(raw, t);
}

/** Helper used by non-Vue infrastructure (e.g. HTTP interceptor) to look up i18n error text safely. */
export function translateErrorKey(key: string, defaultText: string): string {
  return tr(key, defaultText);
}
