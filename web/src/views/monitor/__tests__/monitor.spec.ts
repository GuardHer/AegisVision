import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import Monitor from "../index.vue";

vi.mock("vue-i18n", () => ({
  useI18n: () => ({ t: (k: string) => k })
}));

vi.mock("@/store/modules/user", () => ({
  useUserStoreHook: () => ({ roles: ["admin"] })
}));

vi.mock("@/api/aegis", () => ({
  buildRealtimeWSURL: () => "ws://127.0.0.1/ws/realtime",
  getZonesTree: vi.fn().mockResolvedValue({
    data: [
      {
        id: "z1",
        name: "Workshop-A",
        zone_type: "workshop",
        camera_count: 1,
        online_camera_count: 1,
        children: []
      }
    ]
  }),
  listCameras: vi.fn().mockResolvedValue({
    data: [
      {
        id: "c1",
        name: "Cam-01",
        workstation_code: "WS-01",
        zone_id: "z1",
        status: "online",
        enabled: true
      }
    ]
  }),
  listMonitorPresets: vi.fn().mockResolvedValue({ data: [] }),
  listROIs: vi.fn().mockResolvedValue({ data: [] }),
  createMonitorPreset: vi.fn().mockResolvedValue({ code: 0 }),
  deleteMonitorPreset: vi.fn().mockResolvedValue({ code: 0 }),
  handleAlarm: vi.fn().mockResolvedValue({ code: 0 }),
  evidenceImageURL: (id: string) => `/api/v1/evidence/${id}/image`
}));

class FakeWebSocket {
  static OPEN = 1;
  static lastInstance: FakeWebSocket | null = null;
  readyState = 1;
  onopen: (() => void) | null = null;
  onmessage: ((evt: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  send = vi.fn();
  close = vi.fn();
  constructor() {
    FakeWebSocket.lastInstance = this;
  }
}

describe("Duty Monitor Center", () => {
  it("supports 1/4/9/16 grid switching and sound mute toggle", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket);

    const wrapper = mount(Monitor, {
      global: {
        stubs: {
          WhepPlayer: true,
          CanvasOverlay: true,
          "el-button": { template: "<button><slot /></button>" },
          "el-tag": { template: "<span><slot /></span>" },
          "el-alert": { props: ["title"], template: "<div>{{ title }}<slot /></div>" },
          "el-empty": { template: "<div />" },
          "el-scrollbar": { template: "<div><slot /></div>" },
          "el-tabs": { template: "<div><slot /></div>" },
          "el-tab-pane": { template: "<div><slot /></div>" },
          "el-input": { template: "<div><input /></div>" },
          "el-input-number": { template: "<div><input /></div>" },
          "el-switch": { template: "<input type='checkbox' />" },
          "el-tree": { template: "<div />" },
          "el-select": { template: "<select><slot /></select>" },
          "el-option": { template: "<option />" },
          "el-dialog": { template: "<div><slot /><slot name='footer' /></div>" },
          "el-form": { template: "<form><slot /></form>" },
          "el-form-item": { template: "<div><slot /></div>" },
          "el-radio-group": {
            props: ["modelValue"],
            template: "<div><slot /></div>"
          },
          "el-radio-button": { template: "<button><slot /></button>" }
        }
      }
    });

    await flushPromises();
    expect(wrapper.findAll('[data-test="video-tile"]')).toHaveLength(4);

    // Switch gridSize to 16 via component vm
    (wrapper.vm as any).gridSize = 16;
    await flushPromises();
    expect(wrapper.findAll('[data-test="video-tile"]')).toHaveLength(16);

    // Toggle sound mute
    const soundBtn = wrapper.get('[data-test="sound-toggle"]');
    expect(soundBtn.text()).toContain("aegis.monitor.soundOn");
    await soundBtn.trigger("click");
    expect(soundBtn.text()).toContain("aegis.monitor.soundOff");

    // Emit WS frame_detections with sop_progress -> video tile renders SOP step progress bar
    FakeWebSocket.lastInstance?.onmessage?.({
      data: JSON.stringify({
        type: "frame_detections",
        camera_id: "c1",
        detections: [],
        roi_counts: {},
        sop_progress: [
          {
            rule_id: "sop-1",
            rule_name: "ESD SOP",
            state: "in_progress",
            current_step_index: 1,
            steps: [
              { step_id: "s1", name: "Wear ESD", status: "completed" },
              { step_id: "s2", name: "Scan Barcode", status: "active" }
            ]
          }
        ]
      })
    });
    await flushPromises();
    const sopBar = wrapper.get('[data-test="sop-progress-bar"]');
    expect(sopBar.text()).toContain("ESD SOP");
    expect(sopBar.text()).toContain("Wear ESD");
    expect(sopBar.text()).toContain("Scan Barcode");
  });
});
