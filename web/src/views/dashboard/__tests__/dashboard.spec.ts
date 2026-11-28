import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import Dashboard from "../index.vue";

const mockPush = vi.fn();
const mockOverview = vi.fn().mockResolvedValue({
  code: 0,
  message: "ok",
  data: {
    kpis: {
      today_alarms: 12,
      yesterday_alarms: 9,
      unhandled_alarms: 3,
      confirmed_rate: 80.0,
      false_positive_rate: 20.0,
      today_sop_violations: 2,
      sop_compliance_rate: 94.5,
      cameras_online: 18,
      cameras_total: 20,
      nodes_online: 2,
      nodes_total: 2,
      running_tasks: 14
    },
    trend_7d: [{ date: "2026-11-27", safety_count: 10, sop_count: 2 }],
    top_zones: [{ zone_id: "z1", zone_name: "Workshop-A", alarm_count: 8 }],
    by_rule_mode: [{ rule_mode: "anomaly_detect", count: 10 }],
    recent_alerts: []
  }
});

vi.mock("vue-router", () => ({
  useRouter: () => ({ push: mockPush })
}));

vi.mock("vue-i18n", () => ({
  useI18n: () => ({ t: (k: string) => k })
}));

vi.mock("echarts", () => ({
  init: () => ({
    setOption: vi.fn(),
    resize: vi.fn(),
    dispose: vi.fn()
  })
}));

vi.mock("@/store/modules/user", () => ({
  useUserStoreHook: () => ({ roles: ["admin"] })
}));

vi.mock("@/api/aegis", () => ({
  getDashboardOverview: () => mockOverview(),
  handleAlarm: vi.fn().mockResolvedValue({ code: 0 })
}));

describe("Operations Dashboard", () => {
  const globalStubs = {
    "el-card": { template: '<div @click="$emit(\'click\')"><slot name="header" /><slot /></div>' },
    "el-button": { template: '<button @click="$emit(\'click\')"><slot /></button>' },
    "el-tag": { template: "<span><slot /></span>" },
    "el-empty": { template: "<div />" },
    "el-scrollbar": { template: "<div><slot /></div>" },
    "el-alert": { props: ["title"], template: '<div data-stub="alert">{{ title }}<slot /></div>' }
  };

  it("renders KPIs and navigates to unhandled alarms when clicking KPI card", async () => {
    const wrapper = mount(Dashboard, {
      global: { stubs: globalStubs }
    });

    await flushPromises();
    expect(wrapper.get('[data-test="kpi-today-alarms"]').text()).toBe("12");
    expect(wrapper.get('[data-test="kpi-unhandled-alarms"]').text()).toBe("3");
    expect(wrapper.get('[data-test="kpi-sop-compliance"]').text()).toBe("94.5%");

    await wrapper.get('[data-test="kpi-unhandled-card"]').trigger("click");
    expect(mockPush).toHaveBeenCalledWith({
      path: "/alarms/index",
      query: { handle_status: "unhandled" }
    });
  });

  it("displays inline error alert when loading dashboard overview fails", async () => {
    mockOverview.mockRejectedValueOnce({
      response: { status: 503, data: { message: "analytics backend unavailable" } }
    });

    const wrapper = mount(Dashboard, {
      global: { stubs: globalStubs }
    });

    await flushPromises();
    const errAlert = wrapper.get('[data-test="dashboard-load-error"]');
    expect(errAlert.text()).toContain("analytics backend unavailable");
  });
});
