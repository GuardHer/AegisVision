<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import {
  buildRealtimeWSURL,
  listCameras,
  listROIs,
  type CameraItem,
  type CameraROIItem,
  type DetectionBox
} from "@/api/aegis";
import WhepPlayer from "@/components/aegis/WhepPlayer.vue";
import CanvasOverlay from "@/components/aegis/CanvasOverlay.vue";

defineOptions({ name: "AegisMonitor" });

const { t } = useI18n();

const gridSize = ref<1 | 4 | 9>(4);
const showOverlay = ref(true);
const streamMode = ref<"main" | "sub">("main");
const cameras = ref<CameraItem[]>([]);
const selectedSlots = ref<string[]>(Array(9).fill(""));
const roisByCamera = reactive<Record<string, CameraROIItem[]>>({});
const videoDims = reactive<Record<string, { width: number; height: number }>>({});
const liveFrames = reactive<
  Record<
    string,
    {
      seq: number;
      detections: DetectionBox[];
      roiCounts: Record<string, number>;
    }
  >
>({});

interface RealtimeAlert {
  alarm_id: string;
  camera_id: string;
  camera_name: string;
  rule_mode: string;
  title: string;
  severity: string;
  triggered_at: string;
}

const recentAlerts = ref<RealtimeAlert[]>([]);
const wsConnected = ref(false);
let ws: WebSocket | null = null;
let wsReconnectTimer: ReturnType<typeof setTimeout> | null = null;

const visibleSlots = computed(() => selectedSlots.value.slice(0, gridSize.value));

const gridColsClass = computed(() => {
  if (gridSize.value === 1) return "grid-cols-1";
  if (gridSize.value === 4) return "grid-cols-2";
  return "grid-cols-3";
});

const subscribedCameraIds = computed(() =>
  Array.from(new Set(visibleSlots.value.filter(Boolean)))
);

async function loadCameras() {
  const res = await listCameras();
  cameras.value = res?.data ?? [];
  cameras.value.slice(0, 9).forEach((cam, idx) => {
    if (!selectedSlots.value[idx]) {
      selectedSlots.value[idx] = cam.id;
    }
  });
  await Promise.all(subscribedCameraIds.value.map(loadCameraROIs));
  sendSubscribe();
}

async function loadCameraROIs(camId: string) {
  if (!camId || roisByCamera[camId]) return;
  try {
    const res = await listROIs(camId);
    roisByCamera[camId] = res?.data ?? [];
  } catch {
    roisByCamera[camId] = [];
  }
}

function sendSubscribe() {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(
      JSON.stringify({
        type: "subscribe",
        camera_ids: subscribedCameraIds.value
      })
    );
  }
}

function connectWS() {
  if (ws) {
    ws.close();
    ws = null;
  }
  const socket = new WebSocket(buildRealtimeWSURL());
  ws = socket;

  socket.onopen = () => {
    wsConnected.value = true;
    sendSubscribe();
  };

  socket.onmessage = evt => {
    try {
      const msg = JSON.parse(evt.data);
      if (msg.type === "frame_detections" && msg.camera_id) {
        const prevSeq = liveFrames[msg.camera_id]?.seq ?? 0;
        liveFrames[msg.camera_id] = {
          seq: prevSeq + 1,
          detections: msg.detections ?? [],
          roiCounts: msg.roi_counts ?? {}
        };
      } else if (msg.type === "alarm_alert") {
        recentAlerts.value = [msg, ...recentAlerts.value].slice(0, 50);
      }
    } catch {
      // Ignore malformed WS payloads
    }
  };

  socket.onclose = () => {
    wsConnected.value = false;
    wsReconnectTimer = setTimeout(connectWS, 3000);
  };
}

function onVideoResize(camId: string, size: { width: number; height: number }) {
  videoDims[camId] = size;
}

watch(subscribedCameraIds, ids => {
  ids.forEach(id => void loadCameraROIs(id));
  sendSubscribe();
});

onMounted(() => {
  void loadCameras();
  connectWS();
});

onBeforeUnmount(() => {
  if (wsReconnectTimer) clearTimeout(wsReconnectTimer);
  if (ws) ws.close();
});
</script>

<template>
  <div class="p-4 flex flex-col gap-3 h-full">
    <div class="flex flex-wrap items-center justify-between gap-2 bg-white dark:bg-zinc-900 p-3 rounded border">
      <div class="flex items-center gap-2">
        <el-radio-group v-model="gridSize" size="small">
          <el-radio-button :value="1">{{ t("aegis.monitor.grid1") }}</el-radio-button>
          <el-radio-button :value="4">{{ t("aegis.monitor.grid4") }}</el-radio-button>
          <el-radio-button :value="9">{{ t("aegis.monitor.grid9") }}</el-radio-button>
        </el-radio-group>

        <el-button
          size="small"
          :type="showOverlay ? 'primary' : 'default'"
          @click="showOverlay = !showOverlay"
        >
          {{ showOverlay ? t("aegis.monitor.overlayOn") : t("aegis.monitor.overlayOff") }}
        </el-button>

        <el-radio-group v-model="streamMode" size="small">
          <el-radio-button value="main">{{ t("aegis.monitor.mainStream") }}</el-radio-button>
          <el-radio-button value="sub">{{ t("aegis.monitor.subStream") }}</el-radio-button>
        </el-radio-group>
      </div>

      <el-tag :type="wsConnected ? 'success' : 'danger'" size="small">
        {{ wsConnected ? t("aegis.monitor.wsConnected") : t("aegis.monitor.wsDisconnected") }}
      </el-tag>
    </div>

    <div class="flex gap-3 flex-1 min-h-[540px]">
      <div class="flex-1 grid gap-2" :class="gridColsClass">
        <div
          v-for="(camId, idx) in visibleSlots"
          :key="idx"
          class="relative bg-black rounded overflow-hidden border border-zinc-700 flex flex-col min-h-[240px]"
        >
          <div class="z-10 flex items-center justify-between bg-black/70 px-2 py-1 text-xs text-white">
            <el-select
              v-model="selectedSlots[idx]"
              size="small"
              clearable
              :placeholder="t('aegis.monitor.selectCamera')"
              class="w-48"
            >
              <el-option
                v-for="c in cameras"
                :key="c.id"
                :label="c.name"
                :value="c.id"
              />
            </el-select>
          </div>
          <div class="relative flex-1">
            <template v-if="camId">
              <WhepPlayer
                :camera-id="camId"
                :stream="streamMode"
                @video-resize="sz => onVideoResize(camId, sz)"
              />
              <CanvasOverlay
                :container-width="640"
                :container-height="360"
                :video-width="videoDims[camId]?.width ?? 1920"
                :video-height="videoDims[camId]?.height ?? 1080"
                :visible="showOverlay"
                :frame-seq="liveFrames[camId]?.seq ?? 0"
                :detections="liveFrames[camId]?.detections ?? []"
                :rois="roisByCamera[camId] ?? []"
                :roi-counts="liveFrames[camId]?.roiCounts ?? {}"
              />
            </template>
            <div v-else class="w-full h-full flex items-center justify-center text-zinc-400 text-xs">
              {{ t("aegis.monitor.noCameraSelected") }}
            </div>
          </div>
        </div>
      </div>

      <div class="w-80 bg-white dark:bg-zinc-900 rounded border p-3 flex flex-col overflow-hidden">
        <div class="font-semibold text-sm mb-2">{{ t("aegis.monitor.realtimeAlarms") }}</div>
        <div class="flex-1 overflow-y-auto flex flex-col gap-2">
          <div
            v-for="alert in recentAlerts"
            :key="alert.alarm_id"
            class="p-2 rounded border border-red-200 bg-red-50/40 dark:bg-red-950/30 text-xs"
          >
            <div class="flex items-center justify-between font-medium">
              <span>{{ alert.title }}</span>
              <el-tag size="small" :type="alert.severity === 'critical' ? 'danger' : 'warning'">
                {{ alert.severity }}
              </el-tag>
            </div>
            <div class="text-zinc-500 mt-1">{{ alert.camera_name }} · {{ alert.rule_mode }}</div>
            <div class="text-zinc-400">{{ alert.triggered_at }}</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
