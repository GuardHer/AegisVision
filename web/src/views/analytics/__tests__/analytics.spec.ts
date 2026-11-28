import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import Analytics from "../index.vue";

vi.mock("vue-i18n", () => ({
  useI18n: () => ({ t: (k: string) => k })
}));

vi.mock("echarts", () => ({
  init: vi.fn(() => ({
    setOption: vi.fn(),
    resize: vi.fn(),
    dispose: vi.fn()
  }))
}));

const exportSpy = vi.fn().mockResolvedValue(new Blob(["csv"]));

vi.mock("@/api/aegis", () => ({
  getZonesTree: vi.fn().mockResolvedValue({ data: [] }),
  exportAnalyticsCSVBlob: (...args: any[]) => exportSpy(...args),
  getAnalyticsSummary: vi.fn().mockResolvedValue({
    data: {
      kpis: {
        total_alarms: 12,
        safety_alarms: 8,
        sop_alarms: 4,
        confirmed_count: 9,
        false_positive_count: 3,
        false_positive_rate: 25.0,
        sop_compliant_cycles: 15,
        sop_violated_cycles: 5,
        sop_compliance_rate: 75.0,
        avg_sop_duration_ms: 4200
      },
      trend: [{ bucket: "2026-11-27", safety_count: 8, sop_count: 4 }],
      by_zone: [{ zone_id: "z1", zone_name: "Workshop-1", safety_count: 8, sop_count: 4, confirmed_count: 9, false_positive_count: 3 }],
      top_sop_steps: [{ step_name: "Wear ESD", violation_type: "missing_step", count: 4 }],
      algo_precision: [
        {
          algorithm_version_id: "v1",
          algorithm_name: "SmokingDet",
          version: "1.0.0",
          total_alarms: 12,
          confirmed_count: 9,
          false_positive_count: 3,
          false_positive_rate: 25.0
        }
      ]
    }
  })
}));

describe("Analytics Report Center", () => {
  it("renders SOP compliance KPIs, avg cycle duration, and exports CSV", async () => {
    vi.stubGlobal("URL", {
      createObjectURL: vi.fn(() => "blob:mock"),
      revokeObjectURL: vi.fn()
    });

    const wrapper = mount(Analytics, {
      global: {
        stubs: {
          "el-card": { template: "<div><slot name='header' /><slot /></div>" },
          "el-button": { template: "<button><slot /></button>" },
          "el-radio-group": { template: "<div><slot /></div>" },
          "el-radio-button": { template: "<button><slot /></button>" },
          "el-select": { template: "<select><slot /></select>" },
          "el-option": { template: "<option />" },
          "el-table": { props: ["data"], template: "<div><slot /></div>" },
          "el-table-column": { template: "<div />" },
          "el-date-picker": { template: "<input />" },
          "el-alert": { props: ["title"], template: "<div>{{ title }}<slot /></div>" },
          "el-tag": { template: "<span><slot /></span>" }
        }
      }
    });

    await flushPromises();
    expect(wrapper.get('[data-test="an-total-alarms"]').text()).toBe("12");
    expect(wrapper.get('[data-test="an-sop-compliance"]').text()).toBe("75%");
    expect(wrapper.get('[data-test="an-avg-duration"]').text()).toBe("4.2s");
    expect(wrapper.get('[data-test="an-fp-rate"]').text()).toBe("25%");

    await wrapper.get('[data-test="export-analytics-btn"]').trigger("click");
    await flushPromises();
    expect(exportSpy).toHaveBeenCalledTimes(1);
  });
});
