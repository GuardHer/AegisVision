import { beforeEach, describe, expect, it, vi } from "vitest";
import { ElMessage } from "element-plus";
import {
  extractAsyncErrorMessage,
  extractErrorMessage,
  isCancelError,
  showAsyncError,
  showError
} from "../error";

vi.mock("element-plus", () => ({
  ElMessage: {
    error: vi.fn(),
    success: vi.fn(),
    warning: vi.fn()
  }
}));

describe("error utilities", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("extracts backend envelope message from Axios error response", () => {
    const axiosErr = {
      message: "Request failed with status code 401",
      response: {
        status: 401,
        data: { code: 40100, message: "invalid username or password" }
      }
    };
    expect(extractErrorMessage(axiosErr, "Login failed")).toBe(
      "invalid username or password"
    );
  });

  it("extracts readable status fallback when response has no JSON message", () => {
    const axiosErr = {
      message: "Request failed with status code 502",
      response: {
        status: 502,
        data: "<html><body>502 Bad Gateway</body></html>"
      }
    };
    expect(extractErrorMessage(axiosErr, "Load failed")).toContain("502");
  });

  it("detects network and timeout errors cleanly", () => {
    expect(
      extractErrorMessage({ code: "ERR_NETWORK", message: "Network Error" })
    ).toContain("network");
    expect(
      extractErrorMessage({ code: "ECONNABORTED", message: "timeout of 10000ms exceeded" })
    ).toContain("timeout");
  });

  it("extracts backend JSON error message from Blob responses", async () => {
    const blob = new Blob(
      [JSON.stringify({ code: 40001, message: "export row limit exceeded" })],
      { type: "application/json" }
    );
    const axiosErr = {
      message: "Request failed with status code 400",
      response: {
        status: 400,
        data: blob
      }
    };
    const msg = await extractAsyncErrorMessage(axiosErr, "Export failed");
    expect(msg).toBe("export row limit exceeded");
  });

  it("identifies ElMessageBox cancel/close and Axios cancel errors", () => {
    expect(isCancelError("cancel")).toBe(true);
    expect(isCancelError("close")).toBe(true);
    expect(isCancelError({ isCancelRequest: true })).toBe(true);
    expect(isCancelError({ code: "ERR_CANCELED" })).toBe(true);
    expect(isCancelError(new Error("boom"))).toBe(false);
  });

  it("showError suppresses cancel errors and displays ElMessage.error for real errors", () => {
    expect(showError("cancel", "Delete failed")).toBe("");
    expect(ElMessage.error).not.toHaveBeenCalled();

    const msg = showError(
      { response: { status: 409, data: { message: "camera already exists" } } },
      "Save failed"
    );
    expect(msg).toBe("camera already exists");
    expect(ElMessage.error).toHaveBeenCalledWith(
      expect.objectContaining({
        message: "camera already exists",
        grouping: true,
        showClose: true
      })
    );
  });

  it("showAsyncError decodes Blob JSON and displays ElMessage.error", async () => {
    const blob = new Blob([JSON.stringify({ message: "no matching alarms" })], {
      type: "application/json"
    });
    const msg = await showAsyncError(
      { response: { status: 400, data: blob } },
      "Export failed"
    );
    expect(msg).toBe("no matching alarms");
    expect(ElMessage.error).toHaveBeenCalledWith(
      expect.objectContaining({
        message: "no matching alarms"
      })
    );
  });
});
