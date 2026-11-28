<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { useRoute } from "vue-router";
import { useI18n } from "vue-i18n";
import { ElMessage, ElNotification } from "element-plus";
import {
  batchHandleAlarms,
  evidenceImageURL,
  evidenceVideoURL,
  exportAlarmsBlob,
  getZonesTree,
  handleAlarm,
  listAlarmDeliveries,
  listAlarms,
  listCameras,
  retryDelivery,
  type AlarmDeliveryItem,
  type AlarmItem,
  type CameraItem,
  type ZoneTreeNode
} from "@/api/aegis";
import { useUserStoreHook } from "@/store/modules/user";
import { canHandleAlarms } from "@/utils/permissions";
import { formatDateTime, formatDurationMs } from "@/utils/format";
import { showAsyncError, showError, showSuccess } from "@/utils/error";
import {
  formatChannelType,
  formatDeliveryStatus,
  formatFpReason,
  formatHandleStatus,
  formatRuleMode,
  formatSeverity,
  formatSopStepStatus,
  formatVideoStatus,
  formatViolationType
} from "@/utils/aegisLabels";
import CanvasOverlay from "@/components/aegis/CanvasOverlay.vue";

defineOptions({ name: "AegisAlarms" });

const route = useRoute();
const { t } = useI18n();
const userStore = useUserStoreHook();
const allowHandle = computed(() =>
  (userStore.roles ?? []).some(r => canHandleAlarms(r))
);

const loading = ref(false);
const loadError = ref("");
const exporting = ref(false);
const savingBatch = ref(false);
const savingHandle = ref(false);
const loadingDeliveries = ref(false);
const retryingDeliveryId = ref("");
const zonesTree = ref<ZoneTreeNode[]>([]);
const cameras = ref<CameraItem[]>([]);
const alarms = ref<AlarmItem[]>([]);
const total = ref(0);
const selectedAlarmIds = ref<string[]>([]);

const query = reactive({
  page: 1,
  page_size: 20,
  zone_id: "",
  camera_id: "",
  rule_mode: "",
  severity: "",
  handle_status: "",
  keyword: ""
});

const flatZones = computed(() => {
  const out: Array<{ id: string; name: string; label: string }> = [];
  const walk = (nodes: ZoneTreeNode[], depth: number) => {
    for (const n of nodes) {
      out.push({ id: n.id, name: n.name, label: `${"　".repeat(depth)}${n.name}` });
      if (n.children?.length) walk(n.children, depth + 1);
    }
  };
  walk(zonesTree.value, 0);
  return out;
});

const zoneNameMap = computed(() => {
  const m = new Map<string, string>();
  for (const z of flatZones.value) m.set(z.id, z.name);
  return m;
});

const cameraMap = computed(() => {
  const m = new Map<string, CameraItem>();
  for (const c of cameras.value) m.set(c.id, c);
  return m;
});

function resolveCameraName(row: AlarmItem): string {
  return cameraMap.value.get(row.camera_id)?.name || row.camera_id || "—";
}

function resolveZoneName(row: AlarmItem): string {
  const cam = cameraMap.value.get(row.camera_id);
  if (!cam?.zone_id) return "";
  return zoneNameMap.value.get(cam.zone_id) || "";
}

function severityTagType(severity: string): "danger" | "warning" | "info" {
  if (severity === "critical") return "danger";
  if (severity === "warning") return "warning";
  return "info";
}

function handleStatusTagType(status: string): "danger" | "success" | "warning" | "info" {
  if (status === "unhandled") return "danger";
  if (status === "confirmed") return "success";
  if (status === "false_positive") return "warning";
  return "info";
}

function videoStatusTagType(status: string): "success" | "warning" | "danger" | "info" {
  if (status === "ready") return "success";
  if (status === "recording" || status === "pending") return "warning";
  if (status === "failed") return "danger";
  return "info";
}

const fpReasonOptions = computed(() => [
  { value: "lighting", label: t("aegis.alarms.fpLighting") },
  { value: "occlusion", label: t("aegis.alarms.fpOcclusion") },
  { value: "similar_action", label: t("aegis.alarms.fpSimilarAction") },
  { value: "model_drift", label: t("aegis.alarms.fpModelDrift") },
  { value: "other", label: t("aegis.alarms.fpOther") }
]);

// Batch handle modal
const batchDialogVisible = ref(false);
const batchForm = reactive({
  handle_status: "confirmed",
  false_positive_reason: "lighting",
  handle_note: ""
});

// Single detail drawer
const drawerVisible = ref(false);
const selectedAlarm = ref<AlarmItem | null>(null);
const deliveries = ref<AlarmDeliveryItem[]>([]);
const snapshotContainerRef = ref<HTMLElement | null>(null);
const snapshotContainerSize = reactive({ width: 640, height: 360 });
const snapshotNaturalSize = reactive({ width: 1920, height: 1080 });
let snapshotResizeObserver: ResizeObserver | null = null;

function onSnapshotLoad(evt: Event) {
  const img = evt.target as HTMLImageElement | null;
  if (img && img.naturalWidth > 0 && img.naturalHeight > 0) {
    snapshotNaturalSize.width = img.naturalWidth;
    snapshotNaturalSize.height = img.naturalHeight;
  }
}

watch(snapshotContainerRef, el => {
  if (snapshotResizeObserver) {
    snapshotResizeObserver.disconnect();
    snapshotResizeObserver = null;
  }
  if (!el) return;
  const rect = el.getBoundingClientRect();
  if (rect.width > 0 && rect.height > 0) {
    snapshotContainerSize.width = rect.width;
    snapshotContainerSize.height = rect.height;
  }
  if (typeof ResizeObserver !== "undefined") {
    snapshotResizeObserver = new ResizeObserver(entries => {
      const first = entries[0];
      if (first) {
        snapshotContainerSize.width = first.contentRect.width;
        snapshotContainerSize.height = first.contentRect.height;
      }
    });
    snapshotResizeObserver.observe(el);
  }
});

onBeforeUnmount(() => {
  if (snapshotResizeObserver) {
    snapshotResizeObserver.disconnect();
    snapshotResizeObserver = null;
  }
});

const handleForm = reactive({
  handle_status: "confirmed",
  false_positive_reason: "lighting",
  handle_note: ""
});

async function loadData() {
  loading.value = true;
  try {
    const res = await listAlarms({ ...query });
    alarms.value = res?.data?.items ?? [];
    total.value = res?.data?.total ?? 0;
    loadError.value = "";
  } catch (err: any) {
    loadError.value = showError(err, t("aegis.common.loadFailed"));
  } finally {
    loading.value = false;
  }
}

function searchNow() {
  query.page = 1;
  void loadData();
}

function resetFilters() {
  query.zone_id = "";
  query.camera_id = "";
  query.rule_mode = "";
  query.severity = "";
  query.handle_status = "";
  query.keyword = "";
  query.page = 1;
  void loadData();
}

function onSelectionChange(rows: AlarmItem[]) {
  selectedAlarmIds.value = rows.map(r => r.id);
}

async function openDetail(row: AlarmItem) {
  selectedAlarm.value = row;
  handleForm.handle_status =
    row.handle_status === "unhandled" ? "confirmed" : row.handle_status;
  handleForm.false_positive_reason = row.false_positive_reason || "lighting";
  handleForm.handle_note = row.handle_note || "";
  drawerVisible.value = true;
  loadingDeliveries.value = true;
  try {
    const delRes = await listAlarmDeliveries(row.id);
    deliveries.value = delRes?.data ?? [];
  } catch (err: any) {
    deliveries.value = [];
    showError(err);
  } finally {
    loadingDeliveries.value = false;
  }
}

async function submitHandle() {
  if (!selectedAlarm.value) return;
  savingHandle.value = true;
  try {
    const res = await handleAlarm(selectedAlarm.value.id, {
      handle_status: handleForm.handle_status,
      false_positive_reason:
        handleForm.handle_status === "false_positive"
          ? handleForm.false_positive_reason
          : "",
      handle_note: handleForm.handle_note
    });
    if (res && res.code !== 0) {
      throw new Error(res.message || t("aegis.errors.operationFailed"));
    }
    showSuccess(t("aegis.common.saveSuccess"));
    if (res?.data) {
      selectedAlarm.value = res.data;
    }
    await loadData();
  } catch (err: any) {
    showError(err);
  } finally {
    savingHandle.value = false;
  }
}

async function submitBatchHandle() {
  if (selectedAlarmIds.value.length === 0) return;
  savingBatch.value = true;
  try {
    const res = await batchHandleAlarms({
      alarm_ids: selectedAlarmIds.value,
      handle_status: batchForm.handle_status,
      false_positive_reason:
        batchForm.handle_status === "false_positive"
          ? batchForm.false_positive_reason
          : "",
      handle_note: batchForm.handle_note
    });
    const updatedCount = res?.data?.updated_count ?? selectedAlarmIds.value.length;
    batchDialogVisible.value = false;
    ElNotification({
      title: t("aegis.alarms.batchHandledCount"),
      message: `${updatedCount}`,
      type: "success",
      duration: 3000
    });
    await loadData();
  } catch (err: any) {
    showError(err);
  } finally {
    savingBatch.value = false;
  }
}

async function triggerExport(format: "csv" | "zip") {
  exporting.value = true;
  try {
    const blob = await exportAlarmsBlob({
      format,
      zone_id: query.zone_id,
      camera_id: query.camera_id,
      rule_mode: query.rule_mode,
      severity: query.severity,
      handle_status: query.handle_status,
      keyword: query.keyword
    });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download =
      format === "csv"
        ? `aegis-alarms-${Date.now()}.csv`
        : `aegis-evidence-${Date.now()}.zip`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 10000);
    showSuccess(t("aegis.common.exportSuccess"));
  } catch (err: any) {
    await showAsyncError(err);
  } finally {
    exporting.value = false;
  }
}

async function onRetryDelivery(deliveryId: string) {
  retryingDeliveryId.value = deliveryId;
  try {
    await retryDelivery(deliveryId);
    showSuccess(t("aegis.alarms.retryEnqueued"));
    if (selectedAlarm.value) {
      const delRes = await listAlarmDeliveries(selectedAlarm.value.id);
      deliveries.value = delRes?.data ?? [];
    }
  } catch (err: any) {
    showError(err);
  } finally {
    retryingDeliveryId.value = "";
  }
}

watch(
  () => route.query.handle_status,
  val => {
    const next = typeof val === "string" ? val : "";
    if (query.handle_status !== next) {
      query.handle_status = next;
      query.page = 1;
      void loadData();
    }
  }
);

onMounted(async () => {
  if (typeof route.query.handle_status === "string") {
    query.handle_status = route.query.handle_status;
  }
  const [zoneRes, camRes] = await Promise.allSettled([
    getZonesTree(),
    listCameras()
  ]);
  zonesTree.value = zoneRes.status === "fulfilled" ? (zoneRes.value?.data ?? []) : [];
  cameras.value = camRes.status === "fulfilled" ? (camRes.value?.data ?? []) : [];
  if (zoneRes.status === "rejected" || camRes.status === "rejected") {
    ElMessage.warning({
      message: t("aegis.common.partialLoadWarning"),
      grouping: true
    });
  }
  await loadData();
});
</script>

<template>
  <div class="aegis-page aegis-page--fixed">
    <section class="aegis-panel aegis-panel--fill">
      <header class="aegis-page-header">
        <div class="aegis-page-title-group">
          <h1 class="aegis-page-title">{{ t("menus.aegisAlarms") }}</h1>
        </div>
        <div class="aegis-header-actions">
          <el-button
            v-if="allowHandle"
            type="warning"
            plain
            :disabled="selectedAlarmIds.length === 0"
            @click="batchDialogVisible = true"
          >
            {{ t("aegis.alarms.batchHandle") }} ({{ selectedAlarmIds.length }})
          </el-button>
          <el-button :loading="exporting" @click="triggerExport('csv')">
            {{ t("aegis.alarms.exportCsv") }}
          </el-button>
          <el-button :loading="exporting" @click="triggerExport('zip')">
            {{ t("aegis.alarms.exportZip") }}
          </el-button>
        </div>
      </header>

      <div class="aegis-filter-bar">
        <el-select
          v-model="query.zone_id"
          clearable
          :placeholder="t('aegis.alarms.zone')"
          style="width: 175px"
          @change="searchNow"
        >
          <el-option v-for="z in flatZones" :key="z.id" :label="z.label" :value="z.id" />
        </el-select>
        <el-select
          v-model="query.camera_id"
          clearable
          filterable
          :placeholder="t('aegis.alarms.camera')"
          style="width: 175px"
          @change="searchNow"
        >
          <el-option v-for="c in cameras" :key="c.id" :label="c.name" :value="c.id" />
        </el-select>
        <el-select
          v-model="query.rule_mode"
          clearable
          :placeholder="t('aegis.alarms.ruleMode')"
          style="width: 155px"
          @change="searchNow"
        >
          <el-option :label="formatRuleMode('anomaly_detect', t)" value="anomaly_detect" />
          <el-option :label="formatRuleMode('count_threshold', t)" value="count_threshold" />
          <el-option :label="formatRuleMode('absence_detect', t)" value="absence_detect" />
          <el-option :label="formatRuleMode('sop_sequence', t)" value="sop_sequence" />
        </el-select>
        <el-select
          v-model="query.severity"
          clearable
          :placeholder="t('aegis.alarms.severity')"
          style="width: 120px"
          @change="searchNow"
        >
          <el-option :label="formatSeverity('critical', t)" value="critical" />
          <el-option :label="formatSeverity('warning', t)" value="warning" />
          <el-option :label="formatSeverity('info', t)" value="info" />
        </el-select>
        <el-select
          v-model="query.handle_status"
          clearable
          :placeholder="t('aegis.alarms.handleStatus')"
          style="width: 140px"
          @change="searchNow"
        >
          <el-option :label="formatHandleStatus('unhandled', t)" value="unhandled" />
          <el-option :label="formatHandleStatus('confirmed', t)" value="confirmed" />
          <el-option :label="formatHandleStatus('false_positive', t)" value="false_positive" />
          <el-option :label="formatHandleStatus('ignored', t)" value="ignored" />
        </el-select>
        <el-input
          v-model="query.keyword"
          clearable
          :placeholder="t('aegis.alarms.keyword')"
          style="width: 185px"
          @keyup.enter="searchNow"
        />
        <div class="aegis-filter-bar__end">
          <el-button @click="resetFilters">{{ t("aegis.common.reset") }}</el-button>
          <el-button type="primary" @click="searchNow">
            {{ t("aegis.common.refresh") }}
          </el-button>
        </div>
      </div>

      <el-alert
        v-if="loadError"
        type="error"
        show-icon
        closable
        :title="t('aegis.common.loadFailed')"
        class="mb-3 shrink-0"
        @close="loadError = ''"
      >
        <template #default>
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span>{{ loadError }}</span>
            <el-button size="small" type="danger" plain @click="loadData">
              {{ t("aegis.common.retry") }}
            </el-button>
          </div>
        </template>
      </el-alert>

      <div class="aegis-table-wrap aegis-table-wrap--fill">
        <el-table
          v-loading="loading"
          :data="alarms"
          height="100%"
          border
          stripe
          @selection-change="onSelectionChange"
        >
          <el-table-column type="selection" width="46" />
          <el-table-column
            prop="title"
            :label="t('aegis.alarms.title')"
            min-width="200"
            show-overflow-tooltip
          />
          <el-table-column :label="t('aegis.alarms.camera')" min-width="165" show-overflow-tooltip>
            <template #default="{ row }">
              <div class="truncate font-medium">{{ resolveCameraName(row) }}</div>
              <div v-if="resolveZoneName(row)" class="text-[11px] text-zinc-400 truncate">
                {{ resolveZoneName(row) }}
              </div>
            </template>
          </el-table-column>
          <el-table-column prop="rule_mode" :label="t('aegis.alarms.ruleMode')" width="142">
            <template #default="{ row }">
              <el-tag
                size="small"
                :type="row.rule_mode === 'sop_sequence' ? 'warning' : 'info'"
                effect="plain"
              >
                {{ formatRuleMode(row.rule_mode, t) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="severity" :label="t('aegis.alarms.severity')" width="96">
            <template #default="{ row }">
              <el-tag :type="severityTagType(row.severity)" size="small">
                {{ formatSeverity(row.severity, t) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.alarms.handleStatus')" width="150">
            <template #default="{ row }">
              <div class="flex flex-col items-start gap-0.5">
                <el-tag :type="handleStatusTagType(row.handle_status)" size="small">
                  {{ formatHandleStatus(row.handle_status, t) }}
                </el-tag>
                <span v-if="row.false_positive_reason" class="text-[11px] text-zinc-400">
                  {{ formatFpReason(row.false_positive_reason, t) }}
                </span>
              </div>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.alarms.videoStatus')" width="110">
            <template #default="{ row }">
              <el-tag :type="videoStatusTagType(row.video_status)" size="small" effect="plain">
                {{ formatVideoStatus(row.video_status, t) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.alarms.triggeredAt')" width="170">
            <template #default="{ row }">
              <span class="aegis-tabular">{{ formatDateTime(row.triggered_at) }}</span>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.common.actions')" width="96" fixed="right">
            <template #default="{ row }">
              <el-button link type="primary" @click="openDetail(row)">
                {{ t("aegis.alarms.detail") }}
              </el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <div class="aegis-pagination">
        <el-pagination
          v-model:current-page="query.page"
          v-model:page-size="query.page_size"
          :page-sizes="[20, 50, 100]"
          :total="total"
          layout="total, sizes, prev, pager, next"
          @current-change="loadData"
          @size-change="searchNow"
        />
      </div>
    </section>

    <!-- Batch Handle Modal -->
    <el-dialog v-model="batchDialogVisible" :title="t('aegis.alarms.batchHandle')" width="460px">
      <el-form label-width="110px" size="small">
        <el-form-item :label="t('aegis.alarms.handleStatus')">
          <el-radio-group v-model="batchForm.handle_status">
            <el-radio value="confirmed">{{ t("aegis.alarms.confirm") }}</el-radio>
            <el-radio value="false_positive">{{ t("aegis.alarms.falsePositive") }}</el-radio>
            <el-radio value="ignored">{{ t("aegis.alarms.ignored") }}</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="batchForm.handle_status === 'false_positive'" :label="t('aegis.alarms.fpReason')">
          <el-select v-model="batchForm.false_positive_reason" class="w-full">
            <el-option v-for="opt in fpReasonOptions" :key="opt.value" :label="opt.label" :value="opt.value" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('aegis.alarms.handleNote')">
          <el-input v-model="batchForm.handle_note" type="textarea" :rows="2" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="batchDialogVisible = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" :loading="savingBatch" @click="submitBatchHandle">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>

    <!-- Detail Drawer -->
    <el-drawer v-model="drawerVisible" :title="t('aegis.alarms.detail')" size="52%">
      <template v-if="selectedAlarm">
        <div class="flex flex-col gap-4">
          <div class="flex items-center justify-between gap-2">
            <div class="font-semibold text-base">{{ selectedAlarm.title }}</div>
            <el-tag :type="severityTagType(selectedAlarm.severity)" size="small">
              {{ formatSeverity(selectedAlarm.severity, t) }}
            </el-tag>
          </div>

          <el-descriptions :column="2" size="small" border>
            <el-descriptions-item :label="t('aegis.alarms.camera')">
              {{ resolveCameraName(selectedAlarm) }}
            </el-descriptions-item>
            <el-descriptions-item :label="t('aegis.alarms.zone')">
              {{ resolveZoneName(selectedAlarm) || "—" }}
            </el-descriptions-item>
            <el-descriptions-item :label="t('aegis.alarms.ruleMode')">
              {{ formatRuleMode(selectedAlarm.rule_mode, t) }}
            </el-descriptions-item>
            <el-descriptions-item :label="t('aegis.alarms.triggeredAt')">
              <span class="aegis-tabular">{{ formatDateTime(selectedAlarm.triggered_at) }}</span>
            </el-descriptions-item>
            <el-descriptions-item :label="t('aegis.alarms.handleStatus')">
              <el-tag :type="handleStatusTagType(selectedAlarm.handle_status)" size="small">
                {{ formatHandleStatus(selectedAlarm.handle_status, t) }}
              </el-tag>
            </el-descriptions-item>
            <el-descriptions-item :label="t('aegis.alarms.videoStatus')">
              <el-tag :type="videoStatusTagType(selectedAlarm.video_status)" size="small" effect="plain">
                {{ formatVideoStatus(selectedAlarm.video_status, t) }}
              </el-tag>
            </el-descriptions-item>
          </el-descriptions>

          <div v-if="selectedAlarm.image_object_key">
            <div class="text-xs text-zinc-500 mb-1.5 font-medium">{{ t("aegis.alarms.snapshot") }}</div>
            <div
              ref="snapshotContainerRef"
              class="relative w-full aspect-video bg-[#0b1120] rounded-md overflow-hidden"
            >
              <img
                :src="evidenceImageURL(selectedAlarm.id)"
                :alt="selectedAlarm.title"
                class="w-full h-full object-contain"
                @load="onSnapshotLoad"
              />
              <CanvasOverlay
                :container-width="snapshotContainerSize.width || 640"
                :container-height="snapshotContainerSize.height || 360"
                :video-width="snapshotNaturalSize.width"
                :video-height="snapshotNaturalSize.height"
                :visible="true"
                :stale-timeout-ms="0"
                :detections="selectedAlarm.detections_snapshot ?? []"
                :rois="
                  selectedAlarm.roi_snapshot?.id
                    ? [
                        {
                          id: selectedAlarm.roi_snapshot.id,
                          camera_id: selectedAlarm.camera_id,
                          name: selectedAlarm.roi_snapshot.name ?? 'ROI',
                          roi_type: (selectedAlarm.roi_snapshot.roi_type as any) ?? 'polygon',
                          points: selectedAlarm.roi_snapshot.points ?? [],
                          enabled: true
                        }
                      ]
                    : []
                "
              />
            </div>
          </div>

          <div v-if="selectedAlarm.video_status === 'ready'">
            <div class="text-xs text-zinc-500 mb-1.5 font-medium">{{ t("aegis.alarms.videoClip") }}</div>
            <video
              controls
              preload="metadata"
              playsinline
              class="w-full aspect-video bg-[#0b1120] rounded-md"
              :src="evidenceVideoURL(selectedAlarm.id)"
            />
          </div>
          <el-alert
            v-else-if="selectedAlarm.video_status === 'recording'"
            type="info"
            show-icon
            :closable="false"
            :title="t('aegis.alarms.videoRecordingHint')"
          />
          <el-alert
            v-else-if="selectedAlarm.video_status === 'failed'"
            type="error"
            show-icon
            :closable="false"
            :title="t('aegis.alarms.videoFailedHint')"
          />

          <!-- SOP Violation Step Timeline -->
          <div
            v-if="selectedAlarm.rule_mode === 'sop_sequence' && selectedAlarm.sop_trace"
            class="border rounded-md p-3 bg-amber-50/40 dark:bg-amber-950/20"
            data-test="sop-violation-timeline"
          >
            <div class="flex items-center justify-between mb-2">
              <span class="font-medium text-sm">{{ t("aegis.alarms.sopTimeline") }}</span>
              <div class="flex items-center gap-2 text-xs">
                <el-tag size="small" type="danger">
                  {{ formatViolationType(selectedAlarm.sop_trace.violation_type, t) }}
                </el-tag>
                <span class="text-zinc-500 aegis-tabular">
                  {{ formatDurationMs(selectedAlarm.sop_trace.duration_ms) }}
                </span>
              </div>
            </div>
            <el-timeline>
              <el-timeline-item
                v-for="(step, idx) in selectedAlarm.sop_trace.steps || []"
                :key="step.step_id || idx"
                :type="
                  step.status === 'completed'
                    ? 'success'
                    : step.status === 'violated'
                      ? 'danger'
                      : step.status === 'skipped'
                        ? 'info'
                        : 'primary'
                "
                :timestamp="
                  step.completed_at
                    ? formatDateTime(step.completed_at)
                    : step.elapsed_ms
                      ? formatDurationMs(step.elapsed_ms)
                      : ''
                "
              >
                <div class="flex items-center gap-2 text-xs">
                  <span class="font-medium">#{{ idx + 1 }} {{ step.name }}</span>
                  <el-tag
                    size="small"
                    :type="
                      step.status === 'completed'
                        ? 'success'
                        : step.status === 'violated'
                          ? 'danger'
                          : 'info'
                    "
                  >
                    {{ formatSopStepStatus(step.status, t) }}
                  </el-tag>
                </div>
              </el-timeline-item>
            </el-timeline>
          </div>

          <div v-if="allowHandle" class="border rounded-md p-3">
            <div class="font-medium text-sm mb-2">{{ t("aegis.alarms.handle") }}</div>
            <el-form label-width="110px" size="small">
              <el-form-item :label="t('aegis.alarms.handleStatus')">
                <el-radio-group v-model="handleForm.handle_status">
                  <el-radio value="confirmed">{{ t("aegis.alarms.confirm") }}</el-radio>
                  <el-radio value="false_positive">{{ t("aegis.alarms.falsePositive") }}</el-radio>
                  <el-radio value="ignored">{{ t("aegis.alarms.ignored") }}</el-radio>
                </el-radio-group>
              </el-form-item>
              <el-form-item v-if="handleForm.handle_status === 'false_positive'" :label="t('aegis.alarms.fpReason')">
                <el-select v-model="handleForm.false_positive_reason" class="w-full">
                  <el-option v-for="opt in fpReasonOptions" :key="opt.value" :label="opt.label" :value="opt.value" />
                </el-select>
              </el-form-item>
              <el-form-item :label="t('aegis.alarms.handleNote')">
                <el-input v-model="handleForm.handle_note" type="textarea" :rows="2" />
              </el-form-item>
              <el-form-item>
                <el-button type="primary" :loading="savingHandle" @click="submitHandle">
                  {{ t("aegis.common.save") }}
                </el-button>
              </el-form-item>
            </el-form>
          </div>

          <div>
            <div class="font-medium text-sm mb-2">{{ t("aegis.alarms.deliveries") }}</div>
            <div class="aegis-table-wrap">
              <el-table v-loading="loadingDeliveries" :data="deliveries" size="small" border>
                <template #empty>
                  <el-empty :image-size="48" :description="t('aegis.alarms.noDeliveries')" />
                </template>
                <el-table-column :label="t('aegis.alarms.channelType')" width="120">
                  <template #default="{ row }">
                    {{ formatChannelType(row.channel_type, t) }}
                  </template>
                </el-table-column>
                <el-table-column :label="t('aegis.common.status')" width="106">
                  <template #default="{ row }">
                    <el-tag
                      size="small"
                      :type="row.status === 'success' ? 'success' : row.status === 'failed' ? 'danger' : 'warning'"
                    >
                      {{ formatDeliveryStatus(row.status, t) }}
                    </el-tag>
                  </template>
                </el-table-column>
                <el-table-column prop="attempts" :label="t('aegis.alarms.attempts')" width="88" />
                <el-table-column prop="last_status_code" :label="t('aegis.alarms.httpStatus')" width="82" />
                <el-table-column prop="last_error" :label="t('aegis.alarms.errorDetail')" min-width="140" show-overflow-tooltip />
                <el-table-column v-if="allowHandle" :label="t('aegis.common.actions')" width="100" fixed="right">
                  <template #default="{ row }">
                    <el-button
                      v-if="row.status !== 'success'"
                      link
                      type="primary"
                      :loading="retryingDeliveryId === row.id"
                      @click="onRetryDelivery(row.id)"
                    >
                      {{ t("aegis.alarms.retry") }}
                    </el-button>
                  </template>
                </el-table-column>
              </el-table>
            </div>
          </div>
        </div>
      </template>
    </el-drawer>
  </div>
</template>
