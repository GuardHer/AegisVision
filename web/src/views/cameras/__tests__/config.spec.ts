import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import CameraConfig from "../config.vue";

const createRuleMock = vi.fn().mockResolvedValue({ code: 0, data: { id: "rule-new" } });

vi.mock("vue-router", () => ({
  useRoute: () => ({ params: { id: "cam-1" } }),
  useRouter: () => ({ push: vi.fn() })
}));

vi.mock("vue-i18n", () => ({
  useI18n: () => ({ t: (k: string) => k })
}));

vi.mock("@/api/aegis", () => ({
  getCamera: vi.fn().mockResolvedValue({
    data: { id: "cam-1", name: "Press Cam 01", workstation_code: "WS-01" }
  }),
  listROIs: vi.fn().mockResolvedValue({
    data: [{ id: "roi-1", camera_id: "cam-1", name: "Hazard Zone", roi_type: "polygon", points: [], enabled: true }]
  }),
  listRules: vi.fn().mockResolvedValue({ data: [] }),
  listAlgorithms: vi.fn().mockResolvedValue({
    data: [
      {
        id: "algo-1",
        algorithm_key: "ehs_multi",
        name: "EHS Multi Detector",
        supported_labels: ["no_helmet", "smoking", "fall"]
      }
    ]
  }),
  listAlgorithmVersions: vi.fn().mockResolvedValue({
    data: [
      {
        id: "ver-1",
        algorithm_id: "algo-1",
        version: "v1.0.0",
        runtime_key: "py-ehs-v1",
        status: "active"
      }
    ]
  }),
  listChannels: vi.fn().mockResolvedValue({ data: [] }),
  listSchedules: vi.fn().mockResolvedValue({ data: [] }),
  createROI: vi.fn().mockResolvedValue({ code: 0 }),
  deleteROI: vi.fn().mockResolvedValue({ code: 0 }),
  createRule: (...args: any[]) => createRuleMock(...args),
  updateRule: vi.fn().mockResolvedValue({ code: 0 }),
  deleteRule: vi.fn().mockResolvedValue({ code: 0 })
}));

describe("CameraConfig rule label selector", () => {
  it("populates algorithm supported_labels for multi-select and submits labels array (with custom labels)", async () => {
    const wrapper = mount(CameraConfig, {
      global: {
        stubs: {
          WhepPlayer: true,
          RoiEditor: true,
          "el-alert": { props: ["title"], template: "<div>{{ title }}<slot /></div>" },
          "el-button": { template: "<button v-bind='$attrs'><slot /></button>" },
          "el-tag": { template: "<span><slot /></span>" },
          "el-input": { template: "<input />" },
          "el-input-number": { template: "<input type='number' />" },
          "el-color-picker": { template: "<input type='color' />" },
          "el-switch": { template: "<input type='checkbox' />" },
          "el-radio-group": { template: "<div><slot /></div>" },
          "el-radio": { template: "<label><slot /></label>" },
          "el-checkbox-group": { template: "<div><slot /></div>" },
          "el-checkbox": { template: "<label><slot /></label>" },
          "el-table": { props: ["data"], template: "<div><slot /></div>" },
          "el-table-column": { template: "<div />" },
          "el-pagination": { template: "<div />" },
          "el-dialog": { props: ["modelValue"], template: "<div v-if='modelValue'><slot /><slot name='footer' /></div>" },
          "el-form": { template: "<form><slot /></form>" },
          "el-form-item": { template: "<div><slot /></div>" },
          "el-select": {
            props: ["modelValue", "multiple", "allowCreate"],
            template: "<select v-bind='$attrs'><slot /></select>"
          },
          "el-option": {
            props: ["label", "value"],
            template: "<option :value='value'>{{ label }}</option>"
          }
        },
        directives: { loading: () => {} }
      }
    });

    await flushPromises();
    const vm = wrapper.vm as any;
    vm.openCreateRule();
    await flushPromises();

    // Algorithm version's supported_labels are exposed as dropdown options
    expect(vm.selectedVersionLabels).toEqual(["no_helmet", "smoking", "fall"]);
    const selectEl = wrapper.get('[data-test="rule-labels-select"]');
    const optionValues = selectEl.findAll("option").map(o => o.attributes("value"));
    expect(optionValues).toEqual(["no_helmet", "smoking", "fall"]);

    // Supports selecting preset labels + custom typed label and submits normalized array
    vm.ruleForm.name = "Smoking & Custom";
    vm.ruleForm.labels = ["smoking", "custom_behavior", "smoking"];
    await vm.saveRule();
    await flushPromises();

    expect(createRuleMock).toHaveBeenCalledWith(
      "cam-1",
      expect.objectContaining({
        roi_id: "roi-1",
        algorithm_version_id: "ver-1",
        params: expect.objectContaining({
          labels: ["smoking", "custom_behavior"]
        })
      })
    );
  });
});
