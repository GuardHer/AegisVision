import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { mount } from "@vue/test-utils";
import { nextTick } from "vue";
import CanvasOverlay from "../CanvasOverlay.vue";

describe("CanvasOverlay stale detection cleanup", () => {
  const clearRect = vi.fn();
  const strokeRect = vi.fn();
  const fillRect = vi.fn();
  const fillText = vi.fn();
  const beginPath = vi.fn();
  const moveTo = vi.fn();
  const lineTo = vi.fn();
  const closePath = vi.fn();
  const stroke = vi.fn();
  const fill = vi.fn();
  const measureText = vi.fn(() => ({ width: 50 }));

  beforeEach(() => {
    vi.useFakeTimers();
    clearRect.mockClear();
    strokeRect.mockClear();
    fillRect.mockClear();
    fillText.mockClear();

    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({
      clearRect,
      strokeRect,
      fillRect,
      fillText,
      beginPath,
      moveTo,
      lineTo,
      closePath,
      stroke,
      fill,
      measureText,
      strokeStyle: "",
      fillStyle: "",
      lineWidth: 1,
      font: ""
    } as unknown as CanvasRenderingContext2D);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("draws detection boxes and automatically clears stale detections after 1500ms", async () => {
    const wrapper = mount(CanvasOverlay, {
      props: {
        containerWidth: 800,
        containerHeight: 450,
        videoWidth: 1920,
        videoHeight: 1080,
        visible: true,
        staleTimeoutMs: 1500,
        frameSeq: 1,
        detections: [
          {
            bbox: [0.1, 0.2, 0.4, 0.8],
            label: "no_helmet",
            confidence: 0.92,
            is_abnormal: true
          }
        ],
        rois: [],
        roiCounts: {}
      }
    });

    await nextTick();
    expect(strokeRect).toHaveBeenCalledTimes(1);

    const clearsBefore = clearRect.mock.calls.length;
    strokeRect.mockClear();

    // Advance fake clock past 1500ms stale timeout (1600ms) -> canvas must clear stale detections
    vi.advanceTimersByTime(1600);
    await nextTick();

    expect(clearRect.mock.calls.length).toBeGreaterThan(clearsBefore);
    expect(strokeRect).not.toHaveBeenCalled();

    wrapper.unmount();
  });
});
