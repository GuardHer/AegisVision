<script setup lang="ts">
import { computed, ref } from "vue";
import {
  computeContainedVideoRect,
  normalizedToPixel,
  pixelToNormalized,
  type NormalizedPoint
} from "@/utils/videoRect";

const props = withDefaults(
  defineProps<{
    modelValue: NormalizedPoint[];
    containerWidth: number;
    containerHeight: number;
    videoWidth: number;
    videoHeight: number;
    editable?: boolean;
  }>(),
  {
    editable: true
  }
);

const emit = defineEmits<{
  (e: "update:modelValue", points: NormalizedPoint[]): void;
}>();

const svgRef = ref<SVGSVGElement | null>(null);
const draggingIndex = ref<number | null>(null);

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
  if (!props.editable || draggingIndex.value !== null) return;
  const { x, y } = localCoords(evt);
  const norm = pixelToNormalized(x, y, videoRect.value);
  emit("update:modelValue", [...props.modelValue, norm]);
}

function onVertexPointerDown(index: number, evt: MouseEvent) {
  if (!props.editable) return;
  evt.stopPropagation();
  draggingIndex.value = index;
}

function onSvgMouseMove(evt: MouseEvent) {
  if (draggingIndex.value === null || !props.editable) return;
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
    <polygon
      v-if="pixelPoints.length >= 3"
      :points="polygonAttr"
      fill="rgba(56, 189, 248, 0.22)"
      stroke="#38bdf8"
      stroke-width="2"
    />
    <polyline
      v-else-if="pixelPoints.length === 2"
      :points="polygonAttr"
      fill="none"
      stroke="#38bdf8"
      stroke-width="2"
      stroke-dasharray="4 4"
    />
    <circle
      v-for="(pt, idx) in pixelPoints"
      :key="idx"
      :cx="pt.x"
      :cy="pt.y"
      r="6"
      fill="#0ea5e9"
      stroke="#ffffff"
      stroke-width="2"
      class="cursor-move"
      @mousedown="onVertexPointerDown(idx, $event)"
      @contextmenu="removeVertex(idx, $event)"
    />
  </svg>
</template>
