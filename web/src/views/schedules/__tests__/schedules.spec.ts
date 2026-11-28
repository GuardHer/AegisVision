import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import Schedules from "../index.vue";

vi.mock("vue-i18n", () => ({
  useI18n: () => ({ t: (k: string) => k })
}));

vi.mock("@/api/aegis", () => ({
  listSchedules: vi.fn().mockResolvedValue({
    data: [
      {
        id: "sc-1",
        name: "Night Shift",
        description: "20:00-08:00",
        timezone: "Asia/Shanghai",
        weekly_windows: [{ days: [1, 2, 3, 4, 5], start: "20:00", end: "08:00" }],
        exclude_dates: ["2026-10-01"],
        include_dates: [],
        enabled: true,
        rule_count: 3,
        created_at: "2026-11-27T00:00:00Z"
      }
    ]
  }),
  createSchedule: vi.fn().mockResolvedValue({ code: 0 }),
  updateSchedule: vi.fn().mockResolvedValue({ code: 0 }),
  deleteSchedule: vi.fn().mockResolvedValue({ code: 0 })
}));

describe("Arming Schedules Page", () => {
  it("loads schedule list and opens create modal with default weekly window", async () => {
    const wrapper = mount(Schedules, {
      global: {
        stubs: {
          "el-card": { template: "<div><slot /></div>" },
          "el-button": { template: "<button><slot /></button>" },
          "el-table": { props: ["data"], template: "<div class='tbl'><slot /></div>" },
          "el-table-column": { template: "<div />" },
          "el-tag": { template: "<span><slot /></span>" },
          "el-alert": { props: ["title"], template: "<div>{{ title }}<slot /></div>" },
          "el-pagination": { template: "<div />" },
          "el-dialog": { props: ["modelValue"], template: "<div v-if='modelValue'><slot /><slot name='footer' /></div>" },
          "el-form": { template: "<form><slot /></form>" },
          "el-form-item": { template: "<div><slot /></div>" },
          "el-input": { template: "<input />" },
          "el-switch": { template: "<input type='checkbox' />" },
          "el-checkbox-group": { template: "<div><slot /></div>" },
          "el-checkbox-button": { template: "<span><slot /></span>" }
        }
      }
    });

    await flushPromises();
    expect((wrapper.vm as any).schedules).toHaveLength(1);
    expect((wrapper.vm as any).schedules[0].name).toBe("Night Shift");

    await wrapper.get('[data-test="create-schedule-btn"]').trigger("click");
    await flushPromises();
    expect((wrapper.vm as any).dialogVisible).toBe(true);
    expect((wrapper.vm as any).form.weekly_windows).toHaveLength(1);
  });
});
