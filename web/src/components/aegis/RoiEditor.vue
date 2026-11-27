<script setup lang="ts">
import { computed, ref } from "vue";
import {
  computeContainedVideoRect,
  normalizedToPixel,
  pixelToNormalized,
  type NormalizedPoint
} from "@/utils/videoRect";

import type { CameraROIItem } from "@/api/aegis";

const props = withDefaults(
  defineProps<{
    modelValue: NormalizedPoint[];
    containerWidth: number;
    containerHeight: number;
    videoWidth: number;
    videoHeight: number;
    editable?: boolean;
    strokeColor?: string;
    existingRois?: CameraROIItem[];
  }>(),
  {
    editable: true,
    strokeColor: "#38bdf8",
    existingRois: () => []
  }
);

const emit = defineEmits<{
  (e: "update:modelValue", points: NormalizedPoint[]): void;
}>();

const svgRef = ref<SVGSVGElement | null>(null);
const draggingIndex = ref<number | null>(null);
let suppressNextClick = false;

const videoRect = computed(() =>
  computeContainedVideoRect(
    props.containerWidth,
    props.containerHeight,
    props.videoWidth,
    props.videoHeight
  )
);

const pixelPoints = computed(() =>
  props.modelValue.map(pt => normalizedToPixel(pt, videoRect.value))
);

const polygonAttr = computed(() =>
  pixelPoints.value.map(p => `${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(" ")
);

const existingPolygons = computed(() =>
  (props.existingRois ?? [])
    .filter(r => r.enabled && r.roi_type === "polygon" && (r.points?.length ?? 0) >= 3)
    .map(r => {
      const pts = r.points.map(pt => normalizedToPixel(pt, videoRect.value));
      return {
        id: r.id,
        name: r.name,
        color: r.color || "#38bdf8",
        attr: pts.map(p => `${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(" "),
        labelX: pts[0]?.x ?? 0,
        labelY: Math.max(14, (pts[0]?.y ?? 14) - 6)
      };
    })
);

function localCoords(evt: MouseEvent): { x: number; y: number } {
  const svg = svgRef.value;
  if (!svg) return { x: 0, y: 0 };
  const bounds = svg.getBoundingClientRect();
  return {
    x: evt.clientX - bounds.left,
    y: evt.clientY - bounds.top
  };
}

function onSvgClick(evt: MouseEvent) {
  if (suppressNextClick) {
    suppressNextClick = false;
    return;
  }
  if (!props.editable || draggingIndex.value !== null) return;
  const { x, y } = localCoords(evt);
  const norm = pixelToNormalized(x, y, videoRect.value);
  emit("update:modelValue", [...props.modelValue, norm]);
}

function onVertexPointerDown(index: number, evt: MouseEvent) {
  if (!props.editable) return;
  evt.stopPropagation();
  draggingIndex.value = index;
  suppressNextClick = false;
}

function onSvgMouseMove(evt: MouseEvent) {
  if (draggingIndex.value === null || !props.editable) return;
  suppressNextClick = true;
  const { x, y } = localCoords(evt);
  const norm = pixelToNormalized(x, y, videoRect.value);
  const next = props.modelValue.map((pt, idx) =>
    idx === draggingIndex.value ? norm : pt
  );
  emit("update:modelValue", next);
}

function onSvgMouseUp() {
  draggingIndex.value = null;
}

function removeVertex(index: number, evt: MouseEvent) {
  if (!props.editable) return;
  evt.stopPropagation();
  evt.preventDefault();
  emit(
    "update:modelValue",
    props.modelValue.filter((_, idx) => idx !== index)
  );
}
</script>

<template>
  <svg
    ref="svgRef"
    class="aegis-roi-editor absolute inset-0 w-full h-full select-none"
    :class="editable ? 'cursor-crosshair' : 'pointer-events-none'"
    :viewBox="`0 0 ${Math.max(1, containerWidth)} ${Math.max(1, containerHeight)}`"
    @click="onSvgClick"
    @mousemove="onSvgMouseMove"
    @mouseup="onSvgMouseUp"
    @mouseleave="onSvgMouseUp"
  >
    <!-- Saved camera ROIs in their configured colors -->
    <g v-for="r in existingPolygons" :key="r.id" class="pointer-events-none">
      <polygon
        :points="r.attr"
        :stroke="r.color"
        :fill="r.color"
        fill-opacity="0.14"
        stroke-width="1.8"
        stroke-dasharray="5 3"
      />
      <text
        :x="r.labelX + 4"
        :y="r.labelY"
        :fill="r.color"
        font-size="11"
        font-weight="600"
      >
        {{ r.name }}
      </text>
    </g>

    <polygon
      v-if="pixelPoints.length >= 3"
      :points="polygonAttr"
      :fill="strokeColor"
      fill-opacity="0.22"
      :stroke="strokeColor"
      stroke-width="2"
    />
    <polyline
      v-else-if="pixelPoints.length === 2"
      :points="polygonAttr"
      fill="none"
      :stroke="strokeColor"
      stroke-width="2"
      stroke-dasharray="4 4"
    />
    <circle
      v-for="(pt, idx) in pixelPoints"
      :key="idx"
      :cx="pt.x"
      :cy="pt.y"
      r="6"
      :fill="strokeColor"
      stroke="#ffffff"
      stroke-width="2"
      class="cursor-move"
      @click.stop
      @mousedown="onVertexPointerDown(idx, $event)"
      @contextmenu="removeVertex(idx, $event)"
    />
  </svg>
</template>
