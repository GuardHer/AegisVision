import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import SettingsPage from "../index.vue";

const getSettingsMock = vi.fn().mockResolvedValue({
  code: 0,
  data: {
    platform_title: "AegisVision Factory EHS",
    default_alarm_chime: true,
    popup_min_severity: "warning",
    max_export_rows: 500,
    default_timezone: "Asia/Shanghai"
  }
});

const updateSettingsMock = vi.fn().mockImplementation((payload: any) =>
  Promise.resolve({ code: 0, data: payload })
);

const getHealthMock = vi.fn().mockResolvedValue({
  code: 0,
  data: {
    checked_at: "2026-11-27T08:00:00Z",
    components: [
      { name: "postgresql", status: "healthy", detail: "connected", latency_ms: 2 },
      { name: "redis", status: "healthy", detail: "connected", latency_ms: 1 }
    ],
    cluster: {
      nodes_online: 2,
      nodes_total: 2,
      running_tasks: 5,
      cameras_online: 8,
      cameras_total: 10
    }
  }
});

vi.mock("@/api/aegis", () => ({
  getPlatformSettings: () => getSettingsMock(),
  updatePlatformSettings: (data: any) => updateSettingsMock(data),
  getSystemHealth: () => getHealthMock()
}));

vi.mock("vue-i18n", () => ({
  useI18n: () => ({ t: (k: string) => k })
}));

vi.mock("element-plus", () => ({
  ElMessage: { success: vi.fn(), error: vi.fn() }
}));

describe("SettingsPage", () => {
  it("loads settings & system health and persists settings updates", async () => {
    const wrapper = mount(SettingsPage, {
      global: {
        stubs: {
          "el-row": { template: "<div><slot /></div>" },
          "el-col": { template: "<div><slot /></div>" },
          "el-card": { template: "<div><slot name='header' /><slot /></div>" },
          "el-form": { template: "<form><slot /></form>" },
          "el-form-item": { template: "<div><slot /></div>" },
          "el-input": { template: "<input />" },
          "el-switch": { template: "<input type='checkbox' />" },
          "el-select": { template: "<select><slot /></select>" },
          "el-option": { template: "<option />" },
          "el-input-number": { template: "<input type='number' />" },
          "el-divider": { template: "<hr />" },
          "el-tag": { template: "<span><slot /></span>" },
          "el-alert": { props: ["title"], template: "<div>{{ title }}<slot /></div>" },
          "el-empty": { template: "<div />" },
          "el-button": { template: "<button v-bind='$attrs'><slot /></button>" }
        },
        directives: { loading: () => {} }
      }
    });

    await flushPromises();
    expect(getSettingsMock).toHaveBeenCalledTimes(1);
    expect(getHealthMock).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain("postgresql");
    expect(wrapper.text()).toContain("8 / 10");

    await wrapper.get('[data-testid="save-settings-btn"]').trigger("click");
    await flushPromises();
    expect(updateSettingsMock).toHaveBeenCalledWith(
      expect.objectContaining({
        platform_title: "AegisVision Factory EHS",
        max_export_rows: 500
      })
    );
  });
});
