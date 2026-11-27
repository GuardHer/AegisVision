import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import WhepPlayer from "../WhepPlayer.vue";

vi.mock("vue-i18n", () => ({
  useI18n: () => ({ t: (k: string) => k })
}));

const hlsLoadSource = vi.fn();
const hlsAttachMedia = vi.fn();
const hlsDestroy = vi.fn();
const hlsOn = vi.fn();

vi.mock("hls.js", () => {
  class FakeLoader {
    load() {}
  }
  class FakeHls {
    static isSupported = () => true;
    static DefaultConfig = { loader: FakeLoader };
    static Events = {
      MANIFEST_PARSED: "hlsManifestParsed",
      ERROR: "hlsError"
    };
    static ErrorTypes = {
      NETWORK_ERROR: "networkError",
      MEDIA_ERROR: "mediaError"
    };
    loadSource = hlsLoadSource;
    attachMedia = hlsAttachMedia;
    destroy = hlsDestroy;
    on = hlsOn;
  }
  return { default: FakeHls };
});

const tokenMock = vi.fn();

vi.mock("@/api/aegis", () => ({
  createStreamToken: (...args: any[]) => tokenMock(...args)
}));

describe("WhepPlayer industrial control", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    tokenMock.mockRejectedValue(new Error("WHEP HTTP 503"));
  });

  it("exposes stream switch, zoom, overlay toggle, stats HUD, snapshot, close, and manual reconnect", async () => {
    const wrapper = mount(WhepPlayer, {
      props: {
        cameraId: "cam-1",
        cameraName: "Welding Station 01",
        workstationCode: "WS-01",
        stream: "main",
        showOverlay: true,
        closable: true
      },
      global: {
        stubs: {
          "el-button": { template: "<button><slot /></button>" }
        }
      }
    });

    await flushPromises();
    expect(wrapper.text()).toContain("Welding Station 01");
    expect(wrapper.text()).toContain("WS-01");
    expect(wrapper.text()).toContain("WHEP HTTP 503");

    // Toggle stream quality HD -> SD
    await wrapper.get('[data-test="player-stream-toggle"]').trigger("click");
    expect(wrapper.emitted("update:stream")?.[0]).toEqual(["sub"]);

    // Toggle AI overlay
    await wrapper.get('[data-test="player-overlay-toggle"]').trigger("click");
    expect(wrapper.emitted("update:showOverlay")?.[0]).toEqual([false]);

    // Toggle telemetry HUD
    expect(wrapper.find('[data-test="player-stats-hud"]').exists()).toBe(false);
    await wrapper.get('[data-test="player-stats-toggle"]').trigger("click");
    expect(wrapper.find('[data-test="player-stats-hud"]').exists()).toBe(true);

    // Cycle digital zoom 1x -> 1.5x
    await wrapper.get('[data-test="player-zoom"]').trigger("click");
    expect(wrapper.text()).toContain("1.5×");

    // Emit snapshot & close
    await wrapper.get('[data-test="player-snapshot"]').trigger("click");
    expect(wrapper.emitted("snapshot")).toHaveLength(1);

    await wrapper.get('[data-test="player-close"]').trigger("click");
    expect(wrapper.emitted("close")).toHaveLength(1);
  });

  it("switches to LL-HLS mode for B-frame compatibility and persists camera preference", async () => {
    tokenMock.mockResolvedValue({
      code: 0,
      data: {
        token: "tok-hls-1",
        stream_path: "cam_bframe",
        whep_url: "/whep/cam_bframe/whep?token=tok-hls-1",
        hls_url: "/hls/cam_bframe/index.m3u8?token=tok-hls-1",
        expires_in: 60
      }
    });

    const wrapper = mount(WhepPlayer, {
      props: {
        cameraId: "cam-bframe",
        cameraName: "B-Frame Camera"
      },
      global: {
        stubs: {
          "el-button": { template: "<button><slot /></button>" }
        }
      }
    });

    await flushPromises();
    expect(wrapper.get('[data-test="player-mode-toggle"]').text()).toBe("RTC");

    await wrapper.get('[data-test="player-mode-toggle"]').trigger("click");
    await flushPromises();

    expect(wrapper.emitted("update:mode")?.[0]).toEqual(["hls"]);
    expect(wrapper.get('[data-test="player-mode-toggle"]').text()).toBe("HLS");
    expect(localStorage.getItem("aegis:player-mode:cam-bframe")).toBe("hls");
    expect(hlsLoadSource).toHaveBeenCalledWith("/hls/cam_bframe/index.m3u8?token=tok-hls-1");
    expect(hlsAttachMedia).toHaveBeenCalledTimes(1);

    wrapper.unmount();
    expect(hlsDestroy).toHaveBeenCalledTimes(1);
  });
});
