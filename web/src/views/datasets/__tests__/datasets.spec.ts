import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import DatasetsPage from "../index.vue";

const listFPMock = vi.fn().mockResolvedValue({
  code: 0,
  message: "ok",
  data: {
    items: [
      {
        alarm_id: "alarm-fp-1",
        camera_id: "cam-1",
        camera_name: "Line 1 Cam",
        zone_name: "Workshop A",
        algorithm_name: "Smoking Detector",
        algorithm_version: "v2.1.0",
        rule_mode: "anomaly_detect",
        title: "Smoking Alert",
        severity: "warning",
        triggered_at: "2026-11-27T08:30:00Z",
        false_positive_reason: "lighting",
        handle_note: "sun reflection",
        detections_snapshot: [{ bbox: [0.1, 0.1, 0.3, 0.3], label: "smoking", confidence: 0.78 }],
        roi_snapshot: {},
        image_url: "/api/v1/evidence/alarm-fp-1/image"
      }
    ],
    total: 1,
    page: 1,
    page_size: 24
  }
});

const exportMock = vi.fn().mockResolvedValue(new Blob(["zip-bytes"], { type: "application/zip" }));

vi.mock("@/api/aegis", () => ({
  getZonesTree: vi.fn().mockResolvedValue({ code: 0, data: [] }),
  listFalsePositiveDataset: (...args: any[]) => listFPMock(...args),
  exportFalsePositiveDatasetBlob: (...args: any[]) => exportMock(...args),
  evidenceImageURL: (id: string) => `/api/v1/evidence/${id}/image?token=test`
}));

vi.mock("vue-i18n", () => ({
  useI18n: () => ({ t: (k: string) => k })
}));

vi.mock("element-plus", () => ({
  ElMessage: { success: vi.fn(), error: vi.fn() }
}));

describe("DatasetsPage", () => {
  it("renders false-positive sample cards and triggers ZIP export", async () => {
    const createObjectURL = vi.fn(() => "blob:mock");
    const revokeObjectURL = vi.fn();
    Object.defineProperty(URL, "createObjectURL", { value: createObjectURL, writable: true });
    Object.defineProperty(URL, "revokeObjectURL", { value: revokeObjectURL, writable: true });

    const wrapper = mount(DatasetsPage, {
      global: {
        stubs: {
          "el-card": { template: "<div><slot /></div>" },
          "el-button": { template: "<button v-bind='$attrs'><slot /></button>" },
          "el-tag": { template: "<span><slot /></span>" },
          "el-empty": { template: "<div class='empty' />" },
          "el-tree-select": true,
          "el-select": true,
          "el-option": true,
          "el-date-picker": true,
          "el-pagination": true
        },
        directives: { loading: () => {} }
      }
    });

    await flushPromises();
    expect(listFPMock).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain("Smoking Alert");
    expect(wrapper.text()).toContain("Workshop A");
    expect(wrapper.text()).toContain("sun reflection");

    await wrapper.get('[data-testid="export-dataset-btn"]').trigger("click");
    await flushPromises();
    expect(exportMock).toHaveBeenCalledTimes(1);
  });
});
