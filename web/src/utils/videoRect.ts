export interface NormalizedPoint {
  x: number;
  y: number;
}

export interface VideoRect {
  x: number;
  y: number;
  width: number;
  height: number;
}

/**
 * Computes the actual rendered rectangle of a video inside its container
 * under CSS `object-fit: contain`.
 */
export function computeContainedVideoRect(
  containerWidth: number,
  containerHeight: number,
  videoWidth: number,
  videoHeight: number
): VideoRect {
  if (containerWidth <= 0 || containerHeight <= 0) {
    return { x: 0, y: 0, width: 0, height: 0 };
  }
  if (videoWidth <= 0 || videoHeight <= 0) {
    return { x: 0, y: 0, width: containerWidth, height: containerHeight };
  }

  const containerRatio = containerWidth / containerHeight;
  const videoRatio = videoWidth / videoHeight;

  if (videoRatio > containerRatio) {
    // Letterboxed (black bars top & bottom)
    const width = containerWidth;
    const height = containerWidth / videoRatio;
    const y = (containerHeight - height) / 2;
    return { x: 0, y, width, height };
  }

  // Pillarboxed (black bars left & right)
  const height = containerHeight;
  const width = containerHeight * videoRatio;
  const x = (containerWidth - width) / 2;
  return { x, y: 0, width, height };
}

/**
 * Maps a normalized [0.0, 1.0] point into pixel coordinates within the container.
 */
export function normalizedToPixel(
  pt: NormalizedPoint,
  rect: VideoRect
): { x: number; y: number } {
  return {
    x: rect.x + pt.x * rect.width,
    y: rect.y + pt.y * rect.height
  };
}

/**
 * Maps container pixel coordinates back into clamped normalized [0.0, 1.0] video coordinates.
 */
export function pixelToNormalized(
  px: number,
  py: number,
  rect: VideoRect
): NormalizedPoint {
  if (rect.width <= 0 || rect.height <= 0) {
    return { x: 0, y: 0 };
  }
  const clamp = (v: number) => Math.max(0, Math.min(1, v));
  return {
    x: clamp((px - rect.x) / rect.width),
    y: clamp((py - rect.y) / rect.height)
  };
}
