import { describe, it, expect } from "vitest";
import {
  computeContainedVideoRect,
  normalizedToPixel,
  pixelToNormalized
} from "../videoRect";

describe("videoRect object-fit: contain coordinate mapping", () => {
  it("computes letterboxed rect for 16:9 video inside 4:3 container (top/bottom black bars)", () => {
    // 4:3 container: 800x600; 16:9 video: 1920x1080 -> fitted size 800x450 at y=75
    const rect = computeContainedVideoRect(800, 600, 1920, 1080);
    expect(rect.x).toBeCloseTo(0, 5);
    expect(rect.y).toBeCloseTo(75, 5);
    expect(rect.width).toBeCloseTo(800, 5);
    expect(rect.height).toBeCloseTo(450, 5);

    const topLeft = normalizedToPixel({ x: 0, y: 0 }, rect);
    expect(topLeft).toEqual({ x: 0, y: 75 });

    const bottomRight = normalizedToPixel({ x: 1, y: 1 }, rect);
    expect(bottomRight).toEqual({ x: 800, y: 525 });

    const inverseCenter = pixelToNormalized(400, 300, rect);
    expect(inverseCenter.x).toBeCloseTo(0.5, 5);
    expect(inverseCenter.y).toBeCloseTo(0.5, 5);
  });

  it("computes pillarboxed rect for 16:9 video inside 21:9 container (left/right black bars)", () => {
    // 21:9 container: 2100x900; 16:9 video: 1920x1080 -> fitted size 1600x900 at x=250
    const rect = computeContainedVideoRect(2100, 900, 1920, 1080);
    expect(rect.x).toBeCloseTo(250, 5);
    expect(rect.y).toBeCloseTo(0, 5);
    expect(rect.width).toBeCloseTo(1600, 5);
    expect(rect.height).toBeCloseTo(900, 5);

    const topLeft = normalizedToPixel({ x: 0, y: 0 }, rect);
    expect(topLeft).toEqual({ x: 250, y: 0 });

    const bottomRight = normalizedToPixel({ x: 1, y: 1 }, rect);
    expect(bottomRight).toEqual({ x: 1850, y: 900 });

    // Clicking inside left black bar clamps normalized x to 0
    const clamped = pixelToNormalized(100, 450, rect);
    expect(clamped.x).toBe(0);
    expect(clamped.y).toBeCloseTo(0.5, 5);
  });
});
