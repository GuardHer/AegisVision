<script setup lang="ts">
import {
  computed,
  onBeforeUnmount,
  onMounted,
  reactive,
  ref,
  watch
} from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox, ElNotification } from "element-plus";
import {
  buildRealtimeWSURL,
  createMonitorPreset,
  deleteMonitorPreset,
  evidenceImageURL,
  getZonesTree,
  handleAlarm,
  listCameras,
  listMonitorPresets,
  listROIs,
  type CameraItem,
  type CameraROIItem,
  type DetectionBox,
  type MonitorPresetItem,
  type ZoneTreeNode
} from "@/api/aegis";
import { useUserStoreHook } from "@/store/modules/user";
import { canHandleAlarms } from "@/utils/permissions";
import { playDutyAlertChime } from "@/utils/alertSound";
import { computeContainedVideoRect } from "@/utils/videoRect";
import WhepPlayer from "@/components/aegis/WhepPlayer.vue";
import CanvasOverlay from "@/components/aegis/CanvasOverlay.vue";

defineOptions({ name: "AegisMonitor" });

const { t } = useI18n();
const userStore = useUserStoreHook();
const isAdmin = computed(() => (userStore.roles ?? []).includes("admin"));
const allowHandle = computed(() =>
  (userStore.roles ?? []).some(r => canHandleAlarms(r))
);

const gridSize = ref<1 | 4 | 9 | 16>(4);
const showOverlay = ref(true);
const soundEnabled = ref(true);
const streamMode = ref<"main" | "sub">("main");
const leftCollapsed = ref(false);
const rightCollapsed = ref(false);
const leftTab = ref<"zones" | "presets">("zones");
const treeSearch = ref("");
const alertSeverityFilter = ref<"all" | "critical" | "warning">("all");
const activeSlotIdx = ref(0);

const zonesTree = ref<ZoneTreeNode[]>([]);
const cameras = ref<CameraItem[]>([]);
const presets = ref<MonitorPresetItem[]>([]);
const selectedSlots = ref<string[]>(Array(16).fill(""));
const roisByCamera = reactive<Record<string, CameraROIItem[]>>({});
const videoDims = reactive<Record<string, { width: number; height: number }>>({});
const slotDims = reactive<Record<number, { width: number; height: number }>>({});
const slotElements = new Map<number, HTMLElement>();
const wallContainerRef = ref<HTMLElement | null>(null);
let slotResizeObserver: ResizeObserver | null = null;

// Tour state
const activeTourPreset = ref<MonitorPresetItem | null>(null);
const tourPage = ref(0);
let tourTimer: ReturnType<typeof setInterval> | null = null;

// Save preset dialog
const presetDialogVisible = ref(false);
const presetForm = reactive({
  name: "",
  is_shared: false,
  tour_interval_seconds: 0,
  camera_ids: [] as string[]
});

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
  handled?: boolean;
}

const recentAlerts = ref<RealtimeAlert[]>([]);
const wsConnected = ref(false);
let ws: WebSocket | null = null;
let wsReconnectTimer: ReturnType<typeof setTimeout> | null = null;
let unmounted = false;

const cameraMap = computed(() => {
  const m = new Map<string, CameraItem>();
  for (const c of cameras.value) m.set(c.id, c);
  return m;
});

interface TreeItem {
  id: string;
  label: string;
  isCamera: boolean;
  camera?: CameraItem;
  status?: string;
  children?: TreeItem[];
}

const combinedTree = computed<TreeItem[]>(() => {
  const q = treeSearch.value.trim().toLowerCase();
  const allCamsByZone = new Map<string, CameraItem[]>();
  const unassigned: CameraItem[] = [];

  const camMatches = (cam: CameraItem) =>
    !q ||
    cam.name.toLowerCase().includes(q) ||
    (cam.workstation_code || "").toLowerCase().includes(q) ||
    (cam.location || "").toLowerCase().includes(q);

  for (const cam of cameras.value) {
    if (cam.zone_id) {
      const list = allCamsByZone.get(cam.zone_id) ?? [];
      list.push(cam);
      allCamsByZone.set(cam.zone_id, list);
    } else if (camMatches(cam)) {
      unassigned.push(cam);
    }
  }

  const mapZone = (node: ZoneTreeNode, ancestorMatched: boolean): TreeItem | null => {
    const selfMatch = Boolean(q && node.name.toLowerCase().includes(q));
    const branchMatched = ancestorMatched || selfMatch;
    const childZones = (node.children ?? [])
      .map(ch => mapZone(ch, branchMatched))
      .filter((x): x is TreeItem => x !== null);
    const zoneCams: TreeItem[] = (allCamsByZone.get(node.id) ?? [])
      .filter(cam => branchMatched || camMatches(cam))
      .map(cam => ({
        id: `cam:${cam.id}`,
        label: cam.workstation_code
          ? `${cam.name} [${cam.workstation_code}]`
          : cam.name,
        isCamera: true,
        camera: cam,
        status: cam.status
      }));
    const children = [...childZones, ...zoneCams];
    if (q && !branchMatched && children.length === 0) return null;
    return {
      id: `zone:${node.id}`,
      label: `${node.name} (${node.online_camera_count}/${node.camera_count})`,
      isCamera: false,
      children
    };
  };

  const roots = zonesTree.value
    .map(z => mapZone(z, false))
    .filter((x): x is TreeItem => x !== null);

  if (unassigned.length > 0) {
    roots.push({
      id: "zone:unassigned",
      label: `Unassigned (${unassigned.length})`,
      isCamera: false,
      children: unassigned.map(cam => ({
        id: `cam:${cam.id}`,
        label: cam.workstation_code
          ? `${cam.name} [${cam.workstation_code}]`
          : cam.name,
        isCamera: true,
        camera: cam,
        status: cam.status
      }))
    });
  }
  return roots;
});

const visibleSlots = computed(() => selectedSlots.value.slice(0, gridSize.value));

const gridColsClass = computed(() => {
  if (gridSize.value === 1) return "grid-cols-1";
  if (gridSize.value === 4) return "grid-cols-2";
  if (gridSize.value === 9) return "grid-cols-3";
  return "grid-cols-4";
});

const subscribedCameraIds = computed(() =>
  Array.from(new Set(visibleSlots.value.filter(Boolean)))
);

const filteredAlerts = computed(() => {
  if (alertSeverityFilter.value === "all") return recentAlerts.value;
  return recentAlerts.value.filter(a => a.severity === alertSeverityFilter.value);
});

function setSlotRef(idx: number, el: unknown) {
  const prev = slotElements.get(idx);
  if (el === prev) return;
  if (prev && slotResizeObserver) {
    slotResizeObserver.unobserve(prev);
  }
  if (el instanceof HTMLElement) {
    slotElements.set(idx, el);
    const rect = el.getBoundingClientRect();
    if (rect.width > 0 && rect.height > 0) {
      const cur = slotDims[idx];
      if (!cur || Math.abs(cur.width - rect.width) > 0.5 || Math.abs(cur.height - rect.height) > 0.5) {
        slotDims[idx] = { width: rect.width, height: rect.height };
      }
    }
    if (slotResizeObserver) {
      slotResizeObserver.observe(el);
    }
  } else {
    slotElements.delete(idx);
  }
}

async function loadInitialData() {
  const [zoneRes, camRes, presetRes] = await Promise.all([
    getZonesTree().catch(() => ({ data: [] as ZoneTreeNode[] })),
    listCameras().catch(() => ({ data: [] as CameraItem[] })),
    listMonitorPresets().catch(() => ({ data: [] as MonitorPresetItem[] }))
  ]);
  zonesTree.value = zoneRes?.data ?? [];
  cameras.value = camRes?.data ?? [];
  presets.value = presetRes?.data ?? [];

  cameras.value.slice(0, 16).forEach((cam, idx) => {
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

function onTreeNodeClick(item: TreeItem) {
  if (!item.isCamera || !item.camera) return;
  const idx = activeSlotIdx.value < gridSize.value ? activeSlotIdx.value : 0;
  selectedSlots.value[idx] = item.camera.id;
  activeSlotIdx.value = (idx + 1) % gridSize.value;
}

function applyPreset(preset: MonitorPresetItem) {
  stopTour();
  gridSize.value = preset.grid_size;
  const next = Array(16).fill("");
  (preset.camera_ids ?? []).slice(0, 16).forEach((id, idx) => {
    next[idx] = id;
  });
  selectedSlots.value = next;

  if (preset.tour_interval_seconds > 0 && (preset.camera_ids?.length ?? 0) > preset.grid_size) {
    startTour(preset);
  }
}

function startTour(preset: MonitorPresetItem) {
  stopTour();
  activeTourPreset.value = preset;
  tourPage.value = 0;
  const intervalMs = Math.max(5, preset.tour_interval_seconds || 15) * 1000;
  tourTimer = setInterval(() => {
    const list = preset.camera_ids ?? [];
    if (list.length === 0) return;
    const totalPages = Math.max(1, Math.ceil(list.length / preset.grid_size));
    tourPage.value = (tourPage.value + 1) % totalPages;
    const start = tourPage.value * preset.grid_size;
    const next = Array(16).fill("");
    list.slice(start, start + preset.grid_size).forEach((id, idx) => {
      next[idx] = id;
    });
    selectedSlots.value = next;
  }, intervalMs);
}

function stopTour() {
  if (tourTimer) {
    clearInterval(tourTimer);
    tourTimer = null;
  }
  activeTourPreset.value = null;
}

function openSavePreset() {
  presetForm.name = "";
  presetForm.is_shared = false;
  presetForm.tour_interval_seconds = 0;
  presetForm.camera_ids = Array.from(new Set(selectedSlots.value.filter(Boolean)));
  presetDialogVisible.value = true;
}

async function submitPreset() {
  if (!presetForm.name.trim()) {
    ElMessage.warning(t("aegis.monitor.presetName"));
    return;
  }
  try {
    await createMonitorPreset({
      name: presetForm.name.trim(),
      is_shared: isAdmin.value ? presetForm.is_shared : false,
      grid_size: gridSize.value,
      camera_ids: presetForm.camera_ids,
      tour_interval_seconds: presetForm.tour_interval_seconds
    });
    presetDialogVisible.value = false;
    ElMessage.success("OK");
    const res = await listMonitorPresets();
    presets.value = res?.data ?? [];
  } catch (err: any) {
    ElMessage.error(err?.response?.data?.message || err?.message || "Failed to save preset");
  }
}

async function removePreset(id: string) {
  try {
    await ElMessageBox.confirm(t("aegis.common.confirmDelete"));
    await deleteMonitorPreset(id);
    if (activeTourPreset.value?.id === id) stopTour();
    const res = await listMonitorPresets();
    presets.value = res?.data ?? [];
    ElMessage.success("OK");
  } catch (err: any) {
    if (err === "cancel" || err === "close") return;
    ElMessage.error(err?.response?.data?.message || err?.message || "Failed to delete preset");
  }
}

function toggleFullscreen() {
  const el = wallContainerRef.value;
  if (!el) return;
  if (document.fullscreenElement) {
    void document.exitFullscreen();
  } else {
    void el.requestFullscreen?.();
  }
}

function captureSlotSnapshot(idx: number, camId: string) {
  const container = slotElements.get(idx);
  if (!container) return;
  const video = container.querySelector("video") as HTMLVideoElement | null;
  const overlay = container.querySelector("canvas") as HTMLCanvasElement | null;
  const w = video?.videoWidth || container.clientWidth || 640;
  const h = video?.videoHeight || container.clientHeight || 360;
  const out = document.createElement("canvas");
  out.width = w;
  out.height = h;
  const ctx = out.getContext("2d");
  if (!ctx) return;
  ctx.fillStyle = "#000";
  ctx.fillRect(0, 0, w, h);
  if (video && video.readyState >= 2) {
    ctx.drawImage(video, 0, 0, w, h);
  }
  if (overlay && showOverlay.value && overlay.width > 0 && overlay.height > 0) {
    const crop = computeContainedVideoRect(overlay.width, overlay.height, w, h);
    ctx.drawImage(overlay, crop.x, crop.y, crop.width, crop.height, 0, 0, w, h);
  }
  const link = document.createElement("a");
  const camName = cameraMap.value.get(camId)?.name || camId;
  link.download = `${camName}-${Date.now()}.png`;
  link.href = out.toDataURL("image/png");
  link.click();
}

async function quickHandleAlert(
  alert: RealtimeAlert,
  status: "confirmed" | "false_positive"
) {
  try {
    await handleAlarm(alert.alarm_id, {
      handle_status: status,
      handle_note: "Duty monitor quick handle"
    });
    alert.handled = true;
    ElMessage.success("OK");
  } catch (err: any) {
    ElMessage.error(err?.response?.data?.message || err?.message || "Action failed");
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

function closeWS() {
  if (wsReconnectTimer) {
    clearTimeout(wsReconnectTimer);
    wsReconnectTimer = null;
  }
  if (ws) {
    ws.onopen = null;
    ws.onmessage = null;
    ws.onclose = null;
    ws.close();
    ws = null;
  }
}

function connectWS() {
  if (unmounted) return;
  closeWS();
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
        recentAlerts.value = [msg, ...recentAlerts.value].slice(0, 80);
        if (msg.severity === "critical") {
          if (soundEnabled.value) {
            playDutyAlertChime();
          }
          ElNotification({
            title: msg.title,
            message: `${msg.camera_name} · ${msg.rule_mode}`,
            type: "error",
            duration: 5000
          });
        }
      }
    } catch {
      // Ignore malformed WS payloads
    }
  };

  socket.onclose = () => {
    wsConnected.value = false;
    if (unmounted) return;
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
  unmounted = false;
  if (typeof ResizeObserver !== "undefined") {
    slotResizeObserver = new ResizeObserver(entries => {
      for (const entry of entries) {
        const el = entry.target as HTMLElement;
        for (const [idx, mapped] of slotElements.entries()) {
          if (mapped === el) {
            const w = entry.contentRect.width;
            const h = entry.contentRect.height;
            const cur = slotDims[idx];
            if (!cur || Math.abs(cur.width - w) > 0.5 || Math.abs(cur.height - h) > 0.5) {
              slotDims[idx] = { width: w, height: h };
            }
            break;
          }
        }
      }
    });
    for (const el of slotElements.values()) {
      slotResizeObserver.observe(el);
    }
  }
  void loadInitialData();
  connectWS();
});

onBeforeUnmount(() => {
  unmounted = true;
  stopTour();
  if (slotResizeObserver) {
    slotResizeObserver.disconnect();
    slotResizeObserver = null;
  }
  closeWS();
});
</script>

<template>
  <div ref="wallContainerRef" class="p-3 flex flex-col gap-3 h-full">
    <!-- Duty Control Toolbar -->
    <div class="flex flex-wrap items-center justify-between gap-2 bg-white dark:bg-zinc-900 p-2.5 rounded border">
      <div class="flex flex-wrap items-center gap-2">
        <el-button size="small" @click="leftCollapsed = !leftCollapsed">
          {{ leftCollapsed ? "▶" : "◀" }} {{ t("aegis.monitor.zoneTree") }}
        </el-button>

        <el-radio-group v-model="gridSize" size="small" data-test="grid-selector">
          <el-radio-button :value="1">{{ t("aegis.monitor.grid1") }}</el-radio-button>
          <el-radio-button :value="4">{{ t("aegis.monitor.grid4") }}</el-radio-button>
          <el-radio-button :value="9">{{ t("aegis.monitor.grid9") }}</el-radio-button>
          <el-radio-button :value="16">{{ t("aegis.monitor.grid16") }}</el-radio-button>
        </el-radio-group>

        <el-radio-group v-model="streamMode" size="small">
          <el-radio-button value="main">{{ t("aegis.monitor.mainStream") }}</el-radio-button>
          <el-radio-button value="sub">{{ t("aegis.monitor.subStream") }}</el-radio-button>
        </el-radio-group>

        <el-button
          size="small"
          :type="showOverlay ? 'primary' : 'default'"
          @click="showOverlay = !showOverlay"
        >
          {{ showOverlay ? t("aegis.monitor.overlayOn") : t("aegis.monitor.overlayOff") }}
        </el-button>

        <el-button
          size="small"
          :type="soundEnabled ? 'warning' : 'default'"
          data-test="sound-toggle"
          @click="soundEnabled = !soundEnabled"
        >
          {{ soundEnabled ? t("aegis.monitor.soundOn") : t("aegis.monitor.soundOff") }}
        </el-button>

        <el-button size="small" @click="openSavePreset">
          {{ t("aegis.monitor.savePreset") }}
        </el-button>

        <el-button
          v-if="activeTourPreset"
          size="small"
          type="danger"
          plain
          @click="stopTour"
        >
          {{ t("aegis.monitor.stopTour") }} ({{ activeTourPreset.name }})
        </el-button>

        <el-button size="small" @click="toggleFullscreen">
          {{ t("aegis.monitor.fullscreen") }}
        </el-button>
      </div>

      <div class="flex items-center gap-2">
        <el-tag :type="wsConnected ? 'success' : 'danger'" size="small">
          {{ wsConnected ? t("aegis.monitor.wsConnected") : t("aegis.monitor.wsDisconnected") }}
        </el-tag>
        <el-button size="small" @click="rightCollapsed = !rightCollapsed">
          {{ t("aegis.monitor.realtimeAlarms") }} {{ rightCollapsed ? "◀" : "▶" }}
        </el-button>
      </div>
    </div>

    <!-- 3-Column Duty Workspace -->
    <div class="flex gap-3 flex-1 min-h-[560px]">
      <!-- Left Collapsible Sidebar: Zone Tree + Tour Presets -->
      <div
        v-show="!leftCollapsed"
        class="w-64 shrink-0 bg-white dark:bg-zinc-900 rounded border p-2.5 flex flex-col overflow-hidden"
      >
        <el-tabs v-model="leftTab" stretch>
          <el-tab-pane :label="t('aegis.monitor.zoneTree')" name="zones">
            <el-input
              v-model="treeSearch"
              size="small"
              clearable
              :placeholder="t('aegis.monitor.searchCamera')"
              class="mb-2"
              data-test="zone-tree-search"
            />
            <div class="overflow-y-auto max-h-[500px]">
              <el-tree
                :data="combinedTree"
                node-key="id"
                default-expand-all
                :expand-on-click-node="false"
                @node-click="onTreeNodeClick"
              >
                <template #default="{ data }">
                  <div class="flex items-center gap-1.5 text-xs truncate">
                    <span
                      v-if="data.isCamera"
                      class="w-2 h-2 rounded-full inline-block shrink-0"
                      :class="data.status === 'online' ? 'bg-emerald-500' : 'bg-zinc-400'"
                    />
                    <span class="truncate">{{ data.label }}</span>
                  </div>
                </template>
              </el-tree>
            </div>
          </el-tab-pane>

          <el-tab-pane :label="t('aegis.monitor.presets')" name="presets">
            <div class="flex flex-col gap-2 overflow-y-auto max-h-[520px]">
              <div
                v-for="p in presets"
                :key="p.id"
                class="p-2 rounded border text-xs flex flex-col gap-1 hover:border-blue-400 cursor-pointer"
                @click="applyPreset(p)"
              >
                <div class="flex items-center justify-between font-medium">
                  <span class="truncate">{{ p.name }}</span>
                  <el-tag size="small" type="info">{{ p.grid_size }}G</el-tag>
                </div>
                <div class="flex items-center justify-between text-zinc-400">
                  <span>{{ p.is_shared ? "Shared" : "Private" }}</span>
                  <el-button
                    v-if="!p.is_shared || isAdmin"
                    link
                    type="danger"
                    size="small"
                    @click.stop="removePreset(p.id)"
                  >
                    {{ t("aegis.common.delete") }}
                  </el-button>
                </div>
              </div>
            </div>
          </el-tab-pane>
        </el-tabs>
      </div>

      <!-- Center Video Wall -->
      <div class="flex-1 grid gap-2" :class="gridColsClass" data-test="video-wall">
        <div
          v-for="(camId, idx) in visibleSlots"
          :key="idx"
          data-test="video-tile"
          class="relative bg-black rounded overflow-hidden border flex flex-col min-h-[150px]"
          :class="activeSlotIdx === idx ? 'border-blue-500 ring-1 ring-blue-500' : 'border-zinc-700'"
          @click="activeSlotIdx = idx"
        >
          <div class="z-10 flex items-center justify-between gap-1 bg-black/75 px-2 py-1 text-xs text-white">
            <el-select
              v-model="selectedSlots[idx]"
              size="small"
              clearable
              :placeholder="t('aegis.monitor.selectCamera')"
              class="w-40"
            >
              <el-option
                v-for="c in cameras"
                :key="c.id"
                :label="c.workstation_code ? `${c.name} [${c.workstation_code}]` : c.name"
                :value="c.id"
              />
            </el-select>
            <el-button
              v-if="camId"
              size="small"
              link
              class="text-zinc-200"
              @click.stop="captureSlotSnapshot(idx, camId)"
            >
              {{ t("aegis.monitor.snapshot") }}
            </el-button>
          </div>
          <div :ref="el => setSlotRef(idx, el)" class="relative flex-1">
            <template v-if="camId">
              <WhepPlayer
                :camera-id="camId"
                :stream="streamMode"
                @video-resize="sz => onVideoResize(camId, sz)"
              />
              <CanvasOverlay
                :container-width="slotDims[idx]?.width || 640"
                :container-height="slotDims[idx]?.height || 360"
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

      <!-- Right Collapsible Sidebar: Realtime Alert Stream + Quick Handle -->
      <div
        v-show="!rightCollapsed"
        class="w-80 shrink-0 bg-white dark:bg-zinc-900 rounded border p-3 flex flex-col overflow-hidden"
      >
        <div class="flex items-center justify-between mb-2">
          <span class="font-semibold text-sm">{{ t("aegis.monitor.realtimeAlarms") }}</span>
          <el-select v-model="alertSeverityFilter" size="small" class="w-28">
            <el-option :label="t('aegis.monitor.allSeverities')" value="all" />
            <el-option label="critical" value="critical" />
            <el-option label="warning" value="warning" />
          </el-select>
        </div>

        <div class="flex-1 overflow-y-auto flex flex-col gap-2">
          <div
            v-for="alert in filteredAlerts"
            :key="alert.alarm_id"
            class="p-2 rounded border text-xs flex flex-col gap-1.5"
            :class="
              alert.severity === 'critical'
                ? 'border-red-300 bg-red-50/40 dark:bg-red-950/30'
                : 'border-amber-200 bg-amber-50/40 dark:bg-amber-950/20'
            "
          >
            <div class="flex items-center justify-between font-medium gap-1">
              <span class="truncate">{{ alert.title }}</span>
              <el-tag size="small" :type="alert.severity === 'critical' ? 'danger' : 'warning'">
                {{ alert.severity }}
              </el-tag>
            </div>
            <div class="text-zinc-500">{{ alert.camera_name }} · {{ alert.rule_mode }}</div>
            <img
              :src="evidenceImageURL(alert.alarm_id)"
              class="w-full h-24 object-cover rounded bg-black/80"
              loading="lazy"
            />
            <div class="flex items-center justify-between text-zinc-400">
              <span>{{ alert.triggered_at }}</span>
              <div v-if="allowHandle && !alert.handled" class="flex gap-1">
                <el-button size="small" type="success" link @click="quickHandleAlert(alert, 'confirmed')">
                  {{ t("aegis.alarms.confirm") }}
                </el-button>
                <el-button size="small" type="warning" link @click="quickHandleAlert(alert, 'false_positive')">
                  {{ t("aegis.alarms.falsePositive") }}
                </el-button>
              </div>
              <el-tag v-else-if="alert.handled" size="small" type="success">OK</el-tag>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Save Tour Preset Dialog -->
    <el-dialog v-model="presetDialogVisible" :title="t('aegis.monitor.savePreset')" width="480px">
      <el-form label-width="120px" size="small">
        <el-form-item :label="t('aegis.monitor.presetName')">
          <el-input v-model="presetForm.name" />
        </el-form-item>
        <el-form-item :label="t('aegis.monitor.selectCamera')">
          <el-select
            v-model="presetForm.camera_ids"
            multiple
            filterable
            collapse-tags
            collapse-tags-tooltip
            class="w-full"
          >
            <el-option
              v-for="c in cameras"
              :key="c.id"
              :label="c.workstation_code ? `${c.name} [${c.workstation_code}]` : c.name"
              :value="c.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('aegis.monitor.tourInterval')">
          <el-input-number v-model="presetForm.tour_interval_seconds" :min="0" :max="600" />
        </el-form-item>
        <el-form-item v-if="isAdmin" :label="t('aegis.monitor.sharedPreset')">
          <el-switch v-model="presetForm.is_shared" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="presetDialogVisible = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" @click="submitPreset">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>
