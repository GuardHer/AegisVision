import { ElMessage } from "element-plus";

const GENERIC_AXIOS_STATUS_RE = /^Request failed with status code \d{3}$/i;

const HTTP_STATUS_FALLBACK: Record<number, string> = {
  400: "Bad request (HTTP 400)",
  401: "Authentication required or session expired (HTTP 401)",
  403: "Permission denied (HTTP 403)",
  404: "Requested resource not found (HTTP 404)",
  409: "Resource conflict or already exists (HTTP 409)",
  422: "Validation failed (HTTP 422)",
  429: "Too many requests, please retry later (HTTP 429)",
  500: "Internal server error (HTTP 500)",
  502: "Service gateway unavailable (HTTP 502)",
  503: "Service temporarily unavailable (HTTP 503)",
  504: "Gateway timeout (HTTP 504)"
};

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

/** Extracts a concise, user-facing error message from an Axios/API/runtime error. */
export function extractErrorMessage(
  err: unknown,
  fallback = "Operation failed"
): string {
  if (typeof err === "string") {
    const trimmed = err.trim();
    return trimmed || fallback;
  }
  if (!err || typeof err !== "object") {
    return fallback;
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
    if (backendMsg) return backendMsg;
  } else if (typeof data === "string") {
    const trimmed = data.trim();
    if (trimmed && !isHtmlPayload(trimmed)) {
      return trimmed.length > 240 ? `${trimmed.slice(0, 240)}...` : trimmed;
    }
  }

  const code = typeof anyErr.code === "string" ? anyErr.code : "";
  if (code === "ECONNABORTED" || code === "ETIMEDOUT") {
    return "Request timeout, please check network or retry";
  }
  if (code === "ERR_NETWORK" || anyErr.message === "Network Error") {
    return "Unable to reach server (network error), please check backend service";
  }

  const status = Number(anyErr.response?.status);
  if (Number.isFinite(status) && HTTP_STATUS_FALLBACK[status]) {
    const rawMsg = typeof anyErr.message === "string" ? anyErr.message.trim() : "";
    if (!rawMsg || GENERIC_AXIOS_STATUS_RE.test(rawMsg)) {
      return HTTP_STATUS_FALLBACK[status];
    }
  }

  if (typeof anyErr.message === "string") {
    const trimmed = anyErr.message.trim();
    if (trimmed && !GENERIC_AXIOS_STATUS_RE.test(trimmed)) {
      return trimmed;
    }
  }

  return fallback;
}

/** Async version of extractErrorMessage that also decodes JSON error envelopes inside Blob responses. */
export async function extractAsyncErrorMessage(
  err: unknown,
  fallback = "Operation failed"
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
              return parsed.message.trim();
            }
          } catch {
            if (!isHtmlPayload(trimmed)) {
              return trimmed.length > 240 ? `${trimmed.slice(0, 240)}...` : trimmed;
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
export function showError(err: unknown, fallback = "Operation failed"): string {
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
  fallback = "Operation failed"
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
  return raw;
}
