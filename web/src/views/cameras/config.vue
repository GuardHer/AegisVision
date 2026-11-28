<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  createROI,
  createRule,
  deleteROI,
  deleteRule,
  getCamera,
  listAlgorithms,
  listAlgorithmVersions,
  listChannels,
  listROIs,
  listRules,
  listSchedules,
  updateRule,
  type AlarmChannelItem,
  type AlgorithmVersionItem,
  type ArmingScheduleItem,
  type CameraItem,
  type CameraROIItem,
  type ROIAlgorithmRuleItem
} from "@/api/aegis";
import type { NormalizedPoint } from "@/utils/videoRect";
import WhepPlayer from "@/components/aegis/WhepPlayer.vue";
import RoiEditor from "@/components/aegis/RoiEditor.vue";
import { useRenderIcon } from "@/components/ReIcon/src/hooks";
import { isCancelError, showError, showSuccess } from "@/utils/error";
import {
  formatBehaviorLabel,
  formatBehaviorLabelWithCode,
  formatRoiType,
  formatRuleMode,
  formatScheduleMode,
  formatSeverity,
  formatTriggerMode,
  formatViolationType
} from "@/utils/aegisLabels";
import Back from "~icons/ep/back";

defineOptions({ name: "AegisCameraConfig" });

const route = useRoute();
const router = useRouter();
const { t } = useI18n();
const cameraId = String(route.params.id ?? "");

const camera = ref<CameraItem | null>(null);
const loading = ref(false);
const loadError = ref("");
const savingRoi = ref(false);
const savingRule = ref(false);
const rois = ref<CameraROIItem[]>([]);
const rules = ref<ROIAlgorithmRuleItem[]>([]);
const versions = ref<(AlgorithmVersionItem & { algoName: string; supportedLabels: string[] })[]>([]);
const channels = ref<AlarmChannelItem[]>([]);
const schedules = ref<ArmingScheduleItem[]>([]);

const videoSize = reactive({ width: 1920, height: 1080 });
const editorContainerRef = ref<HTMLElement | null>(null);
const editorContainerSize = reactive({ width: 800, height: 450 });
let editorResizeObserver: ResizeObserver | null = null;
const frozenDataURL = ref<string | null>(null);

const draftRoiName = ref("");
const draftRoiColor = ref("#38bdf8");
const draftPoints = ref<NormalizedPoint[]>([]);

const IconBack = useRenderIcon(Back);
const rulePage = ref(1);
const rulePageSize = ref(10);
const pagedRules = computed(() => {
  const start = (rulePage.value - 1) * rulePageSize.value;
  return rules.value.slice(start, start + rulePageSize.value);
});

const scheduleNameMap = computed(() => {
  const m = new Map<string, string>();
  for (const s of schedules.value) m.set(s.id, s.name);
  return m;
});

const roiNameMap = computed(() => {
  const m = new Map<string, string>();
  for (const r of rois.value) m.set(r.id, r.name);
  return m;
});

interface SOPStepForm {
  step_id: string;
  name: string;
  labels: string[];
  roi_id: string;
  min_confidence: number;
  hit_point: string;
  min_dwell_frames: number;
  max_wait_seconds: number;
  required: boolean;
}

const ruleDialogVisible = ref(false);
const editingRuleId = ref<string | null>(null);
const ruleForm = reactive({
  roi_id: "",
  algorithm_version_id: "",
  name: "",
  rule_mode: "anomaly_detect" as
    | "anomaly_detect"
    | "count_threshold"
    | "absence_detect"
    | "sop_sequence",
  schedule_id: "",
  labels: ["person"] as string[],
  min_confidence: 0.55,
  consecutive_frames: 3,
  duration_seconds: 10,
  cooldown_seconds: 30,
  heartbeat_timeout_seconds: 5,
  min_count: 1,
  max_count: 5,
  severity: "warning",
  schedule_mode: "auto",
  pinned_node_id: "",
  record_video: false,
  video_pre_seconds: 5,
  video_post_seconds: 5,
  algo_params_override: {} as Record<string, any>,
  enabled: true,
  channel_ids: [] as string[],
  trigger_mode: "first_step" as "first_step" | "zone_entry",
  entry_labels: ["person"] as string[],
  rearm_cooldown_seconds: 30,
  cycle_timeout_seconds: 120,
  violation_types: ["missing_step", "out_of_order", "step_timeout"] as string[],
  steps: [] as SOPStepForm[]
});

const selectedVersionLabels = computed<string[]>(() => {
  const ver = versions.value.find(v => v.id === ruleForm.algorithm_version_id);
  const raw = ver?.supportedLabels ?? [];
  return Array.from(new Set(raw.map(s => s.trim()).filter(Boolean)));
});

function summarizeRuleLabels(row: ROIAlgorithmRuleItem): string[] {
  const p = row.params ?? {};
  const set = new Set<string>();
  if (Array.isArray(p.labels)) {
    for (const l of p.labels) if (typeof l === "string" && l.trim()) set.add(l.trim());
  }
  if (Array.isArray(p.steps)) {
    for (const st of p.steps) {
      if (Array.isArray(st?.labels)) {
        for (const l of st.labels) if (typeof l === "string" && l.trim()) set.add(l.trim());
      }
    }
  }
  return Array.from(set);
}

function normalizeLabels(list: string[]): string[] {
  return Array.from(new Set((list ?? []).map(s => String(s).trim()).filter(Boolean)));
}

function createDefaultSOPSteps(): SOPStepForm[] {
  return [
    {
      step_id: "step_1",
      name: "Step 1",
      labels: ["wear_esd_strap"],
      roi_id: "",
      min_confidence: 0.6,
      hit_point: "center",
      min_dwell_frames: 2,
      max_wait_seconds: 15,
      required: true
    },
    {
      step_id: "step_2",
      name: "Step 2",
      labels: ["scan_barcode"],
      roi_id: "",
      min_confidence: 0.6,
      hit_point: "center",
      min_dwell_frames: 1,
      max_wait_seconds: 20,
      required: true
    }
  ];
}

async function loadAll() {
  loading.value = true;
  try {
    const [camRes, roiRes, ruleRes, algoRes, chRes, schedRes] = await Promise.all([
      getCamera(cameraId),
      listROIs(cameraId),
      listRules(cameraId),
      listAlgorithms(),
      listChannels(),
      listSchedules().catch(() => ({ data: [] as ArmingScheduleItem[] }))
    ]);
    camera.value = camRes?.data ?? null;
    rois.value = roiRes?.data ?? [];
    rules.value = ruleRes?.data ?? [];
    channels.value = chRes?.data ?? [];
    schedules.value = schedRes?.data ?? [];

    const algos = algoRes?.data ?? [];
    const versionGroups = await Promise.all(
      algos.map(async algo => {
        const vRes = await listAlgorithmVersions(algo.id).catch(() => ({ data: [] as AlgorithmVersionItem[] }));
        return (vRes?.data ?? []).map(v => ({
          ...v,
          algoName: `${algo.name} (${v.version})`,
          supportedLabels: Array.isArray(algo.supported_labels) ? algo.supported_labels : []
        }));
      })
    );
    versions.value = versionGroups.flat();
    loadError.value = "";
  } catch (err: any) {
    loadError.value = showError(err, t("aegis.common.loadFailed"));
  } finally {
    loading.value = false;
  }
}

function toggleFreezeFrame() {
  if (frozenDataURL.value) {
    frozenDataURL.value = null;
    return;
  }
  const video = editorContainerRef.value?.querySelector("video") as HTMLVideoElement | null;
  if (!video || video.readyState < 2 || video.videoWidth === 0) {
    ElMessage.warning(t("aegis.errors.streamNotReady"));
    return;
  }
  const c = document.createElement("canvas");
  c.width = video.videoWidth;
  c.height = video.videoHeight;
  const ctx = c.getContext("2d");
  if (!ctx) return;
  ctx.drawImage(video, 0, 0, c.width, c.height);
  frozenDataURL.value = c.toDataURL("image/jpeg", 0.9);
}

async function savePolygonROI() {
  if (!draftRoiName.value.trim() || draftPoints.value.length < 3) {
    ElMessage.warning(t("aegis.errors.roiVerticesRequired"));
    return;
  }
  savingRoi.value = true;
  try {
    await createROI(cameraId, {
      name: draftRoiName.value.trim(),
      roi_type: "polygon",
      points: draftPoints.value,
      color: draftRoiColor.value
    });
    draftRoiName.value = "";
    draftPoints.value = [];
    showSuccess(t("aegis.common.saveSuccess"));
    await loadAll();
  } catch (err: any) {
    showError(err);
  } finally {
    savingRoi.value = false;
  }
}

async function saveFullFrameROI() {
  savingRoi.value = true;
  try {
    await createROI(cameraId, {
      name: draftRoiName.value.trim() || t("aegis.cameras.defaultFullFrameName"),
      roi_type: "full_frame",
      points: [],
      color: draftRoiColor.value
    });
    draftRoiName.value = "";
    showSuccess(t("aegis.common.saveSuccess"));
    await loadAll();
  } catch (err: any) {
    showError(err);
  } finally {
    savingRoi.value = false;
  }
}

async function removeROI(id: string) {
  try {
    await ElMessageBox.confirm(
      t("aegis.common.confirmDelete"),
      t("aegis.common.confirmTitle"),
      { type: "warning" }
    );
    await deleteROI(id);
    showSuccess(t("aegis.common.deleteSuccess"));
    await loadAll();
  } catch (err: any) {
    if (isCancelError(err)) return;
    showError(err);
  }
}

function openCreateRule() {
  editingRuleId.value = null;
  const firstVer = versions.value[0];
  const initialLabels =
    firstVer?.supportedLabels?.length ? [firstVer.supportedLabels[0]] : ["person"];
  Object.assign(ruleForm, {
    roi_id: rois.value[0]?.id ?? "",
    algorithm_version_id: firstVer?.id ?? "",
    name: "",
    rule_mode: "anomaly_detect",
    schedule_id: "",
    labels: initialLabels,
    min_confidence: 0.55,
    consecutive_frames: 3,
    duration_seconds: 10,
    cooldown_seconds: 30,
    heartbeat_timeout_seconds: 5,
    min_count: 1,
    max_count: 5,
    severity: "warning",
    schedule_mode: "auto",
    pinned_node_id: "",
    record_video: false,
    video_pre_seconds: 5,
    video_post_seconds: 5,
    algo_params_override: {},
    enabled: true,
    channel_ids: [],
    trigger_mode: "first_step",
    entry_labels: ["person"],
    rearm_cooldown_seconds: 30,
    cycle_timeout_seconds: 120,
    violation_types: ["missing_step", "out_of_order", "step_timeout"],
    steps: createDefaultSOPSteps()
  });
  ruleDialogVisible.value = true;
}

function openEditRule(row: ROIAlgorithmRuleItem) {
  editingRuleId.value = row.id;
  const p = row.params ?? {};
  Object.assign(ruleForm, {
    roi_id: row.roi_id,
    algorithm_version_id: row.algorithm_version_id,
    name: row.name,
    rule_mode: row.rule_mode,
    schedule_id: row.schedule_id || "",
    labels: Array.isArray(p.labels) ? normalizeLabels(p.labels) : [],
    min_confidence: p.min_confidence ?? 0.55,
    consecutive_frames: p.consecutive_frames ?? 3,
    duration_seconds: p.duration_seconds ?? 10,
    cooldown_seconds: p.cooldown_seconds ?? 30,
    heartbeat_timeout_seconds: p.heartbeat_timeout_seconds ?? 5,
    min_count: p.min_count ?? 1,
    max_count: p.max_count ?? 5,
    severity: p.severity ?? "warning",
    schedule_mode: row.schedule_mode || "auto",
    pinned_node_id: row.pinned_node_id || "",
    record_video: row.record_video,
    video_pre_seconds: row.video_pre_seconds ?? 5,
    video_post_seconds: row.video_post_seconds ?? 5,
    algo_params_override: row.algo_params_override ?? {},
    enabled: row.enabled,
    channel_ids: [],
    trigger_mode: p.trigger_mode ?? "first_step",
    entry_labels: Array.isArray(p.entry_labels) ? normalizeLabels(p.entry_labels) : ["person"],
    rearm_cooldown_seconds: p.rearm_cooldown_seconds ?? 30,
    cycle_timeout_seconds: p.cycle_timeout_seconds ?? 120,
    violation_types: Array.isArray(p.violation_types)
      ? [...p.violation_types]
      : ["missing_step", "out_of_order", "step_timeout"],
    steps:
      Array.isArray(p.steps) && p.steps.length > 0
        ? p.steps.map((s: any, idx: number) => ({
            step_id: s.step_id || `step_${idx + 1}`,
            name: s.name || `Step ${idx + 1}`,
            labels: Array.isArray(s.labels) ? normalizeLabels(s.labels) : [],
            roi_id: s.roi_id || "",
            min_confidence: s.min_confidence ?? 0.6,
            hit_point: s.hit_point || "center",
            min_dwell_frames: s.min_dwell_frames ?? 1,
            max_wait_seconds: s.max_wait_seconds ?? 15,
            required: s.required !== false
          }))
        : createDefaultSOPSteps()
  });
  ruleDialogVisible.value = true;
}

function addSOPStep() {
  const n = ruleForm.steps.length + 1;
  ruleForm.steps.push({
    step_id: `step_${n}_${Date.now().toString(36).slice(-3)}`,
    name: `Step ${n}`,
    labels: [],
    roi_id: "",
    min_confidence: 0.6,
    hit_point: "center",
    min_dwell_frames: 1,
    max_wait_seconds: 15,
    required: true
  });
}

function moveSOPStep(idx: number, delta: -1 | 1) {
  const target = idx + delta;
  if (target < 0 || target >= ruleForm.steps.length) return;
  const tmp = ruleForm.steps[idx];
  ruleForm.steps[idx] = ruleForm.steps[target];
  ruleForm.steps[target] = tmp;
}

function removeSOPStep(idx: number) {
  ruleForm.steps.splice(idx, 1);
}

async function saveRule() {
  if (
    !ruleForm.name.trim() ||
    (!editingRuleId.value && (!ruleForm.roi_id || !ruleForm.algorithm_version_id))
  ) {
    ElMessage.warning(t("aegis.cameras.ruleFieldsRequired"));
    return;
  }
  const params: Record<string, any> = {
    cooldown_seconds: ruleForm.cooldown_seconds,
    heartbeat_timeout_seconds: ruleForm.heartbeat_timeout_seconds,
    severity: ruleForm.severity
  };

  if (ruleForm.rule_mode === "sop_sequence") {
    params.trigger_mode = ruleForm.trigger_mode;
    params.entry_labels = normalizeLabels(ruleForm.entry_labels);
    params.rearm_mode = "exit_or_cooldown";
    params.rearm_cooldown_seconds = ruleForm.rearm_cooldown_seconds;
    params.cycle_timeout_seconds = ruleForm.cycle_timeout_seconds;
    params.violation_types = ruleForm.violation_types;
    params.steps = ruleForm.steps.map(s => ({
      step_id: s.step_id.trim(),
      name: s.name.trim(),
      labels: normalizeLabels(s.labels),
      roi_id: s.roi_id,
      min_confidence: s.min_confidence,
      hit_point: s.hit_point || "center",
      min_dwell_frames: s.min_dwell_frames,
      max_wait_seconds: s.max_wait_seconds,
      required: s.required
    }));
  } else {
    params.labels = normalizeLabels(ruleForm.labels);
    params.min_confidence = ruleForm.min_confidence;
    params.consecutive_frames = ruleForm.consecutive_frames;
    params.duration_seconds = ruleForm.duration_seconds;
    if (ruleForm.rule_mode === "count_threshold") {
      params.min_count = ruleForm.min_count;
      params.max_count = ruleForm.max_count;
    }
    if (ruleForm.rule_mode === "absence_detect") {
      params.min_count = ruleForm.min_count;
    }
  }

  savingRule.value = true;
  try {
    if (editingRuleId.value) {
      await updateRule(editingRuleId.value, {
        name: ruleForm.name.trim(),
        rule_mode: ruleForm.rule_mode,
        params,
        algo_params_override: ruleForm.algo_params_override,
        schedule_mode: ruleForm.schedule_mode,
        pinned_node_id: ruleForm.pinned_node_id,
        schedule_id: ruleForm.schedule_id,
        record_video: ruleForm.record_video,
        video_pre_seconds: ruleForm.video_pre_seconds,
        video_post_seconds: ruleForm.video_post_seconds,
        enabled: ruleForm.enabled,
        ...(ruleForm.channel_ids.length > 0 ? { channel_ids: ruleForm.channel_ids } : {})
      });
    } else {
      await createRule(cameraId, {
        roi_id: ruleForm.roi_id,
        algorithm_version_id: ruleForm.algorithm_version_id,
        name: ruleForm.name.trim(),
        rule_mode: ruleForm.rule_mode,
        params,
        algo_params_override: ruleForm.algo_params_override,
        schedule_mode: ruleForm.schedule_mode,
        pinned_node_id: ruleForm.pinned_node_id,
        schedule_id: ruleForm.schedule_id,
        record_video: ruleForm.record_video,
        video_pre_seconds: ruleForm.video_pre_seconds,
        video_post_seconds: ruleForm.video_post_seconds,
        enabled: ruleForm.enabled,
        channel_ids: ruleForm.channel_ids
      });
    }
    ruleDialogVisible.value = false;
    showSuccess(t("aegis.common.saveSuccess"));
    await loadAll();
  } catch (err: any) {
    showError(err);
  } finally {
    savingRule.value = false;
  }
}

async function removeRule(id: string) {
  try {
    await ElMessageBox.confirm(
      t("aegis.common.confirmDelete"),
      t("aegis.common.confirmTitle"),
      { type: "warning" }
    );
    await deleteRule(id);
    showSuccess(t("aegis.common.deleteSuccess"));
    await loadAll();
  } catch (err: any) {
    if (isCancelError(err)) return;
    showError(err);
  }
}

onMounted(() => {
  void loadAll();
  const el = editorContainerRef.value;
  if (el) {
    const rect = el.getBoundingClientRect();
    if (rect.width > 0 && rect.height > 0) {
      editorContainerSize.width = rect.width;
      editorContainerSize.height = rect.height;
    }
    if (typeof ResizeObserver !== "undefined") {
      editorResizeObserver = new ResizeObserver(entries => {
        const first = entries[0];
        if (first) {
          const w = Math.round(first.contentRect.width);
          const h = Math.round(first.contentRect.height);
          if (w > 0 && h > 0 && (Math.abs(editorContainerSize.width - w) > 1 || Math.abs(editorContainerSize.height - h) > 1)) {
            editorContainerSize.width = w;
            editorContainerSize.height = h;
          }
        }
      });
      editorResizeObserver.observe(el);
    }
  }
});

onBeforeUnmount(() => {
  if (editorResizeObserver) {
    editorResizeObserver.disconnect();
    editorResizeObserver = null;
  }
});
</script>

<template>
  <div class="aegis-page">
    <el-alert
      v-if="loadError"
      type="error"
      show-icon
      closable
      :title="t('aegis.common.loadFailed')"
      @close="loadError = ''"
    >
      <template #default>
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span>{{ loadError }}</span>
          <el-button size="small" type="danger" plain @click="loadAll">
            {{ t("aegis.common.retry") }}
          </el-button>
        </div>
      </template>
    </el-alert>

    <!-- ROI Visual Workspace -->
    <section v-loading="loading" class="aegis-panel">
      <header class="aegis-page-header">
        <div class="aegis-page-title-group">
          <div class="flex flex-wrap items-center gap-2">
            <el-button size="small" :icon="IconBack" @click="router.push('/cameras/index')">
              {{ t("aegis.common.back") }}
            </el-button>
            <h1 class="aegis-page-title">
              {{ camera?.name || cameraId }} — {{ t("aegis.cameras.roiSection") }}
            </h1>
            <el-tag v-if="camera?.workstation_code" size="small" type="info" effect="plain">
              {{ camera.workstation_code }}
            </el-tag>
          </div>
          <p class="aegis-page-subtitle">{{ t("aegis.cameras.roiHint") }}</p>
        </div>
        <div class="aegis-header-actions">
          <el-button size="small" :type="frozenDataURL ? 'warning' : 'default'" @click="toggleFreezeFrame">
            {{ frozenDataURL ? t("aegis.cameras.unfreezeFrame") : t("aegis.cameras.freezeFrame") }}
          </el-button>
        </div>
      </header>

      <div class="roi-studio-grid">
        <!-- Left: Responsive 16:9 Canvas + Draw Controls -->
        <div class="min-w-0 flex flex-col gap-3">
          <div ref="editorContainerRef" class="roi-stage">
            <img
              v-if="frozenDataURL"
              :src="frozenDataURL"
              class="absolute inset-0 w-full h-full object-contain"
              alt="Frozen camera frame"
            />
            <WhepPlayer
              v-else-if="cameraId"
              class="roi-stage__player"
              :camera-id="cameraId"
              :camera-name="camera?.name"
              controls="minimal"
              @video-resize="sz => Object.assign(videoSize, sz)"
            />
            <RoiEditor
              v-model="draftPoints"
              :container-width="editorContainerSize.width || 800"
              :container-height="editorContainerSize.height || 450"
              :video-width="videoSize.width"
              :video-height="videoSize.height"
              :stroke-color="draftRoiColor"
              :existing-rois="rois"
            />
          </div>

          <div class="aegis-filter-bar !mb-0">
            <el-input v-model="draftRoiName" :placeholder="t('aegis.cameras.roiNamePlaceholder')" style="width: 195px" size="small" />
            <el-color-picker v-model="draftRoiColor" size="small" />
            <el-button type="primary" size="small" :loading="savingRoi" @click="savePolygonROI">
              {{ t("aegis.cameras.addPolygonRoi") }} ({{ draftPoints.length }})
            </el-button>
            <el-button size="small" :loading="savingRoi" @click="saveFullFrameROI">
              {{ t("aegis.cameras.addFullFrameRoi") }}
            </el-button>
            <el-button size="small" @click="draftPoints = []">
              {{ t("aegis.cameras.clearPoints") }}
            </el-button>
          </div>
        </div>

        <!-- Right: Configured ROIs List -->
        <div class="min-w-0 flex flex-col gap-2">
          <div class="text-xs font-semibold text-zinc-500">
            {{ t("aegis.cameras.roiSection") }} ({{ rois.length }})
          </div>
          <div class="aegis-table-wrap">
            <el-table :data="rois" size="small" max-height="380" border stripe>
              <el-table-column prop="name" :label="t('aegis.cameras.roi')" min-width="115" show-overflow-tooltip />
              <el-table-column :label="t('aegis.cameras.roiColor')" width="64" align="center">
                <template #default="{ row }">
                  <span
                    class="inline-block w-4 h-4 rounded-full border align-middle"
                    :style="{ backgroundColor: row.color || '#38bdf8' }"
                  />
                </template>
              </el-table-column>
              <el-table-column prop="roi_type" :label="t('aegis.cameras.roiType')" width="104">
                <template #default="{ row }">
                  <el-tag size="small" type="info" effect="plain">{{ formatRoiType(row.roi_type, t) }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column :label="t('aegis.common.actions')" width="78" fixed="right">
                <template #default="{ row }">
                  <el-button link type="danger" @click="removeROI(row.id)">
                    {{ t("aegis.common.delete") }}
                  </el-button>
                </template>
              </el-table-column>
            </el-table>
          </div>
        </div>
      </div>
    </section>

    <!-- Rules Section -->
    <section class="aegis-panel">
      <header class="aegis-page-header">
        <div class="aegis-page-title-group">
          <h2 class="aegis-page-title">{{ t("aegis.cameras.ruleSection") }}</h2>
        </div>
        <div class="aegis-header-actions">
          <el-button type="primary" size="small" @click="openCreateRule">
            {{ t("aegis.cameras.addRule") }}
          </el-button>
        </div>
      </header>

      <el-alert
        v-if="!loading && rois.length === 0"
        type="warning"
        show-icon
        :closable="false"
        :title="t('aegis.cameras.noRoiWarning')"
        class="mb-3"
      />

      <div class="aegis-table-wrap">
        <el-table :data="pagedRules" size="small" max-height="420" border stripe>
          <el-table-column prop="name" :label="t('aegis.cameras.ruleName')" min-width="160" show-overflow-tooltip />
          <el-table-column :label="t('aegis.cameras.roi')" min-width="120" show-overflow-tooltip>
            <template #default="{ row }">
              {{ roiNameMap.get(row.roi_id) || "—" }}
            </template>
          </el-table-column>
          <el-table-column prop="rule_mode" :label="t('aegis.alarms.ruleMode')" width="142">
            <template #default="{ row }">
              <el-tag :type="row.rule_mode === 'sop_sequence' ? 'warning' : 'info'" size="small">
                {{ formatRuleMode(row.rule_mode, t) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.cameras.targetLabels')" min-width="180">
            <template #default="{ row }">
              <div class="flex flex-wrap gap-1">
                <el-tooltip
                  v-for="lbl in summarizeRuleLabels(row)"
                  :key="lbl"
                  :content="formatBehaviorLabelWithCode(lbl, t)"
                  placement="top"
                >
                  <el-tag size="small" type="primary" effect="plain">
                    {{ formatBehaviorLabel(lbl, t) }}
                  </el-tag>
                </el-tooltip>
                <span v-if="summarizeRuleLabels(row).length === 0" class="text-xs text-zinc-400">—</span>
              </div>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.cameras.armingSchedule')" min-width="145" show-overflow-tooltip>
            <template #default="{ row }">
              {{ row.schedule_id ? scheduleNameMap.get(row.schedule_id) || row.schedule_id : t("aegis.cameras.alwaysArmed") }}
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.cameras.scheduleMode')" width="102">
            <template #default="{ row }">
              {{ formatScheduleMode(row.schedule_mode, t) }}
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.cameras.recordVideo')" width="110">
            <template #default="{ row }">
              <el-tag :type="row.record_video ? 'success' : 'info'" size="small" effect="plain">
                {{ row.record_video ? t("aegis.common.yes") : t("aegis.common.no") }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.common.enabled')" width="84">
            <template #default="{ row }">
              <el-tag :type="row.enabled ? 'success' : 'danger'" size="small">
                {{ row.enabled ? t("aegis.common.enabled") : t("aegis.common.disabled") }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.common.actions')" width="136" fixed="right">
            <template #default="{ row }">
              <el-button link type="primary" @click="openEditRule(row)">
                {{ t("aegis.common.edit") }}
              </el-button>
              <el-button link type="danger" @click="removeRule(row.id)">
                {{ t("aegis.common.delete") }}
              </el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <div v-if="rules.length > rulePageSize" class="aegis-pagination">
        <el-pagination
          v-model:current-page="rulePage"
          v-model:page-size="rulePageSize"
          :page-sizes="[10, 20, 50]"
          :total="rules.length"
          layout="total, sizes, prev, pager, next"
          @size-change="rulePage = 1"
        />
      </div>
    </section>

    <!-- Create / Edit Rule Dialog (with Visual SOP Sequence Orchestrator) -->
    <el-dialog
      v-model="ruleDialogVisible"
      :title="editingRuleId ? t('aegis.common.edit') : t('aegis.cameras.addRule')"
      width="740px"
    >
      <el-form label-width="145px" size="small">
        <el-form-item :label="t('aegis.cameras.ruleName')">
          <el-input v-model="ruleForm.name" />
        </el-form-item>
        <el-form-item v-if="!editingRuleId" :label="t('aegis.cameras.roi')">
          <el-select v-model="ruleForm.roi_id" class="w-full">
            <el-option v-for="r in rois" :key="r.id" :label="r.name" :value="r.id" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="!editingRuleId" :label="t('aegis.cameras.algorithmVersion')">
          <el-select v-model="ruleForm.algorithm_version_id" class="w-full">
            <el-option v-for="v in versions" :key="v.id" :label="v.algoName" :value="v.id" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('aegis.alarms.ruleMode')">
          <el-select v-model="ruleForm.rule_mode" class="w-full">
            <el-option :label="formatRuleMode('anomaly_detect', t)" value="anomaly_detect" />
            <el-option :label="formatRuleMode('count_threshold', t)" value="count_threshold" />
            <el-option :label="formatRuleMode('absence_detect', t)" value="absence_detect" />
            <el-option :label="formatRuleMode('sop_sequence', t)" value="sop_sequence" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.armingSchedule')">
          <el-select
            v-model="ruleForm.schedule_id"
            clearable
            class="w-full"
            :placeholder="t('aegis.cameras.alwaysArmed')"
          >
            <el-option v-for="s in schedules" :key="s.id" :label="s.name" :value="s.id" />
          </el-select>
        </el-form-item>

        <!-- Standard 3-Mode Params -->
        <template v-if="ruleForm.rule_mode !== 'sop_sequence'">
          <el-form-item :label="t('aegis.cameras.targetLabels')">
            <el-select
              v-model="ruleForm.labels"
              multiple
              filterable
              allow-create
              default-first-option
              clearable
              class="w-full"
              :placeholder="t('aegis.cameras.selectOrInputLabels')"
              data-test="rule-labels-select"
            >
              <el-option
                v-for="lbl in selectedVersionLabels"
                :key="lbl"
                :label="formatBehaviorLabelWithCode(lbl, t)"
                :value="lbl"
              />
            </el-select>
          </el-form-item>
          <el-form-item :label="t('aegis.cameras.confidenceAndFrames')">
            <div class="flex gap-2">
              <el-input-number v-model="ruleForm.min_confidence" :min="0.1" :max="1" :step="0.05" />
              <el-input-number v-model="ruleForm.consecutive_frames" :min="1" :max="30" />
            </div>
          </el-form-item>
          <el-form-item v-if="ruleForm.rule_mode === 'count_threshold' || ruleForm.rule_mode === 'absence_detect'" :label="t('aegis.cameras.minMaxCount')">
            <div class="flex gap-2">
              <el-input-number v-model="ruleForm.min_count" :min="0" />
              <el-input-number v-if="ruleForm.rule_mode === 'count_threshold'" v-model="ruleForm.max_count" :min="1" />
            </div>
          </el-form-item>
          <el-form-item :label="t('aegis.cameras.durationAndCooldown')">
            <div class="flex gap-2">
              <el-input-number v-model="ruleForm.duration_seconds" :min="0" />
              <el-input-number v-model="ruleForm.cooldown_seconds" :min="0" />
            </div>
          </el-form-item>
        </template>

        <!-- Visual Multi-Step SOP Orchestrator -->
        <template v-else>
          <el-form-item :label="t('aegis.cameras.triggerMode')">
            <el-radio-group v-model="ruleForm.trigger_mode">
              <el-radio value="first_step">{{ formatTriggerMode("first_step", t) }}</el-radio>
              <el-radio value="zone_entry">{{ formatTriggerMode("zone_entry", t) }}</el-radio>
            </el-radio-group>
          </el-form-item>
          <el-form-item v-if="ruleForm.trigger_mode === 'zone_entry'" :label="t('aegis.cameras.entryLabels')">
            <div class="flex gap-2 w-full">
              <el-select
                v-model="ruleForm.entry_labels"
                multiple
                filterable
                allow-create
                default-first-option
                clearable
                class="flex-1"
                :placeholder="t('aegis.cameras.selectOrInputLabels')"
              >
                <el-option
                  v-for="lbl in selectedVersionLabels"
                  :key="lbl"
                  :label="formatBehaviorLabelWithCode(lbl, t)"
                  :value="lbl"
                />
              </el-select>
              <el-input-number v-model="ruleForm.rearm_cooldown_seconds" :min="1" :max="600" />
            </div>
          </el-form-item>
          <el-form-item :label="t('aegis.cameras.cycleTimeout')">
            <div class="flex gap-2">
              <el-input-number v-model="ruleForm.cycle_timeout_seconds" :min="5" :max="3600" />
              <el-input-number v-model="ruleForm.cooldown_seconds" :min="0" :max="600" />
            </div>
          </el-form-item>
          <el-form-item :label="t('aegis.cameras.violationTypes')">
            <el-checkbox-group v-model="ruleForm.violation_types">
              <el-checkbox value="missing_step">{{ formatViolationType("missing_step", t) }}</el-checkbox>
              <el-checkbox value="out_of_order">{{ formatViolationType("out_of_order", t) }}</el-checkbox>
              <el-checkbox value="step_timeout">{{ formatViolationType("step_timeout", t) }}</el-checkbox>
            </el-checkbox-group>
          </el-form-item>

          <el-form-item :label="t('aegis.cameras.sopSteps')">
            <div class="w-full flex flex-col gap-2">
              <div
                v-for="(step, idx) in ruleForm.steps"
                :key="step.step_id"
                class="p-2.5 rounded border bg-zinc-50/50 dark:bg-zinc-900/50 flex flex-col gap-2"
              >
                <div class="flex items-center justify-between gap-2">
                  <span class="font-medium text-xs">#{{ idx + 1 }} ({{ step.step_id }})</span>
                  <div class="flex items-center gap-1">
                    <el-switch v-model="step.required" :active-text="t('aegis.cameras.stepRequired')" />
                    <el-button size="small" link :disabled="idx === 0" :aria-label="t('aegis.cameras.moveStepUp')" @click="moveSOPStep(idx, -1)">↑</el-button>
                    <el-button size="small" link :disabled="idx === ruleForm.steps.length - 1" :aria-label="t('aegis.cameras.moveStepDown')" @click="moveSOPStep(idx, 1)">↓</el-button>
                    <el-button size="small" link type="danger" :disabled="ruleForm.steps.length <= 1" :aria-label="t('aegis.cameras.removeStep')" @click="removeSOPStep(idx)">×</el-button>
                  </div>
                </div>
                <div class="grid grid-cols-2 gap-2">
                  <el-input v-model="step.name" :placeholder="t('aegis.cameras.stepName')" />
                  <el-select
                    v-model="step.labels"
                    multiple
                    filterable
                    allow-create
                    default-first-option
                    clearable
                    :placeholder="t('aegis.cameras.stepLabels')"
                  >
                    <el-option
                      v-for="lbl in selectedVersionLabels"
                      :key="lbl"
                      :label="formatBehaviorLabelWithCode(lbl, t)"
                      :value="lbl"
                    />
                  </el-select>
                </div>
                <div class="grid grid-cols-3 gap-2">
                  <el-select v-model="step.roi_id" clearable :placeholder="t('aegis.cameras.stepRoi')">
                    <el-option v-for="r in rois" :key="r.id" :label="r.name" :value="r.id" />
                  </el-select>
                  <el-input-number v-model="step.min_dwell_frames" :min="1" :max="30" :placeholder="t('aegis.cameras.stepDwell')" />
                  <el-input-number v-model="step.max_wait_seconds" :min="0" :max="600" :placeholder="t('aegis.cameras.stepMaxWait')" />
                </div>
              </div>
              <el-button size="small" @click="addSOPStep">+ {{ t("aegis.cameras.addStep") }}</el-button>
            </div>
          </el-form-item>
        </template>

        <el-form-item :label="t('aegis.alarms.severity')">
          <el-select v-model="ruleForm.severity" class="w-40">
            <el-option :label="formatSeverity('critical', t)" value="critical" />
            <el-option :label="formatSeverity('warning', t)" value="warning" />
            <el-option :label="formatSeverity('info', t)" value="info" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.recordVideo')">
          <el-switch v-model="ruleForm.record_video" />
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.channels')">
          <el-select v-model="ruleForm.channel_ids" multiple class="w-full">
            <el-option v-for="ch in channels" :key="ch.id" :label="ch.name" :value="ch.id" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="ruleDialogVisible = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" :loading="savingRule" @click="saveRule">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
.roi-studio-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.65fr) minmax(300px, 1fr);
  gap: 16px;
  align-items: start;
}

.roi-stage {
  position: relative;
  width: 100%;
  aspect-ratio: 16 / 9;
  background: #0b1120;
  border-radius: 6px;
  overflow: hidden;
}

.roi-stage__player {
  position: absolute;
  inset: 0;
}

@media (max-width: 1100px) {
  .roi-studio-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
