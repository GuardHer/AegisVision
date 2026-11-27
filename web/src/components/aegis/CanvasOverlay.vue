<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import type { CameraROIItem, DetectionBox } from "@/api/aegis";
import { computeContainedVideoRect, normalizedToPixel } from "@/utils/videoRect";

const props = withDefaults(
  defineProps<{
    containerWidth: number;
    containerHeight: number;
    videoWidth: number;
    videoHeight: number;
    visible?: boolean;
    staleTimeoutMs?: number;
    frameSeq?: number;
    detections?: DetectionBox[];
    rois?: CameraROIItem[];
    roiCounts?: Record<string, number>;
  }>(),
  {
    visible: true,
    staleTimeoutMs: 1500,
    frameSeq: 0,
    detections: () => [],
    rois: () => [],
    roiCounts: () => ({})
  }
);

const canvasRef = ref<HTMLCanvasElement | null>(null);
const staleCleared = ref(false);
let staleTimer: ReturnType<typeof setTimeout> | null = null;

function resetStaleTimer() {
  if (staleTimer) {
    clearTimeout(staleTimer);
    staleTimer = null;
  }
  staleCleared.value = false;
  if (props.staleTimeoutMs > 0) {
    staleTimer = setTimeout(() => {
      staleCleared.value = true;
      render();
    }, props.staleTimeoutMs);
  }
}

function render() {
  const canvas = canvasRef.value;
  if (!canvas) return;
  const ctx = canvas.getContext("2d");
  if (!ctx) return;

  const cw = Math.max(1, Math.floor(props.containerWidth));
  const ch = Math.max(1, Math.floor(props.containerHeight));
  if (canvas.width !== cw) canvas.width = cw;
  if (canvas.height !== ch) canvas.height = ch;

  ctx.clearRect(0, 0, cw, ch);
  if (!props.visible) return;

  const rect = computeContainedVideoRect(cw, ch, props.videoWidth, props.videoHeight);

  // 1. Draw configured ROI polygons and live counts
  for (const roi of props.rois) {
    if (!roi.enabled) continue;
    const count = props.roiCounts[roi.id];
    if (roi.roi_type === "polygon" && roi.points && roi.points.length >= 3) {
      ctx.beginPath();
      const first = normalizedToPixel(roi.points[0], rect);
      ctx.moveTo(first.x, first.y);
      for (let i = 1; i < roi.points.length; i++) {
        const pt = normalizedToPixel(roi.points[i], rect);
        ctx.lineTo(pt.x, pt.y);
      }
      ctx.closePath();
      ctx.strokeStyle = "#38bdf8";
      ctx.lineWidth = 2;
      ctx.stroke();
      ctx.fillStyle = "rgba(56, 189, 248, 0.12)";
      ctx.fill();

      const label = count !== undefined ? `${roi.name}: ${count}` : roi.name;
      ctx.font = "12px sans-serif";
      ctx.fillStyle = "#38bdf8";
      ctx.fillText(label, first.x + 4, Math.max(14, first.y - 4));
    } else if (roi.roi_type === "full_frame" && count !== undefined) {
      ctx.font = "12px sans-serif";
      ctx.fillStyle = "#38bdf8";
      ctx.fillText(`${roi.name}: ${count}`, rect.x + 8, rect.y + 18);
    }
  }

  // 2. Draw active detection boxes unless stale timeout elapsed
  if (staleCleared.value) return;

  for (const det of props.detections) {
    if (!det.bbox || det.bbox.length < 4) continue;
    const p1 = normalizedToPixel({ x: det.bbox[0], y: det.bbox[1] }, rect);
    const p2 = normalizedToPixel({ x: det.bbox[2], y: det.bbox[3] }, rect);
    const w = p2.x - p1.x;
    const h = p2.y - p1.y;

    const color = det.is_abnormal ? "#ef4444" : "#22c55e";
    ctx.strokeStyle = color;
    ctx.lineWidth = 2;
    ctx.strokeRect(p1.x, p1.y, w, h);

    const text = `${det.label} ${(det.confidence * 100).toFixed(0)}%`;
    ctx.font = "12px sans-serif";
    const textW = ctx.measureText(text).width + 8;
    const labelY = Math.max(0, p1.y - 18);
    ctx.fillStyle = color;
    ctx.fillRect(p1.x, labelY, textW, 18);
    ctx.fillStyle = "#ffffff";
    ctx.fillText(text, p1.x + 4, labelY + 13);
  }
}

watch(
  () => [props.frameSeq, props.detections],
  () => {
    resetStaleTimer();
    render();
  },
  { deep: true }
);

watch(
  () => [
    props.containerWidth,
    props.containerHeight,
    props.videoWidth,
    props.videoHeight,
    props.visible,
    props.rois,
    props.roiCounts
  ],
  () => render(),
  { deep: true }
);

onMounted(() => {
  if (props.detections.length > 0 || props.frameSeq > 0) {
    resetStaleTimer();
  }
  render();
});

onBeforeUnmount(() => {
  if (staleTimer) clearTimeout(staleTimer);
});
</script>

<template>
  <canvas
    ref="canvasRef"
    class="aegis-canvas-overlay absolute inset-0 w-full h-full pointer-events-none"
  />
</template>
