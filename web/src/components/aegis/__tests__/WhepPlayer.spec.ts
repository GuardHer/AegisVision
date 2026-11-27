import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import WhepPlayer from "../WhepPlayer.vue";

vi.mock("vue-i18n", () => ({
  useI18n: () => ({ t: (k: string) => k })
}));

const tokenMock = vi.fn().mockRejectedValue(new Error("WHEP HTTP 503"));

vi.mock("@/api/aegis", () => ({
  createStreamToken: (...args: any[]) => tokenMock(...args)
}));

describe("WhepPlayer industrial control", () => {
  it("exposes stream switch, zoom, overlay toggle, stats HUD, snapshot, close, and manual reconnect", async () => {
    const wrapper = mount(WhepPlayer, {
      props: {
        cameraId: "cam-1",
        cameraName: "Welding Station 01",
        workstationCode: "WS-01",
        stream: "main",
        showOverlay: true,
        closable: true
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
});
