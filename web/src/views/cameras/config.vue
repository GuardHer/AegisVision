<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref } from "vue";
import { useRoute } from "vue-router";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
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
  type AlarmChannelItem,
  type AlgorithmVersionItem,
  type CameraItem,
  type CameraROIItem,
  type ROIAlgorithmRuleItem
} from "@/api/aegis";
import type { NormalizedPoint } from "@/utils/videoRect";
import WhepPlayer from "@/components/aegis/WhepPlayer.vue";
import RoiEditor from "@/components/aegis/RoiEditor.vue";

defineOptions({ name: "AegisCameraConfig" });

const route = useRoute();
const { t } = useI18n();
const cameraId = String(route.params.id ?? "");

const camera = ref<CameraItem | null>(null);
const rois = ref<CameraROIItem[]>([]);
const rules = ref<ROIAlgorithmRuleItem[]>([]);
const versions = ref<(AlgorithmVersionItem & { algoName: string })[]>([]);
const channels = ref<AlarmChannelItem[]>([]);

const videoSize = reactive({ width: 1920, height: 1080 });
const editorContainerRef = ref<HTMLElement | null>(null);
const editorContainerSize = reactive({ width: 800, height: 450 });
let editorResizeObserver: ResizeObserver | null = null;
const draftRoiName = ref("");
const draftPoints = ref<NormalizedPoint[]>([]);

const ruleDialogVisible = ref(false);
const ruleForm = reactive({
  roi_id: "",
  algorithm_version_id: "",
  name: "",
  rule_mode: "anomaly_detect" as "anomaly_detect" | "count_threshold" | "absence_detect",
  labels: "person",
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
  channel_ids: [] as string[]
});

async function loadAll() {
  const [camRes, roiRes, ruleRes, algoRes, chRes] = await Promise.all([
    getCamera(cameraId),
    listROIs(cameraId),
    listRules(cameraId),
    listAlgorithms(),
    listChannels()
  ]);
  camera.value = camRes?.data ?? null;
  rois.value = roiRes?.data ?? [];
  rules.value = ruleRes?.data ?? [];
  channels.value = chRes?.data ?? [];

  const allVers: (AlgorithmVersionItem & { algoName: string })[] = [];
  for (const algo of algoRes?.data ?? []) {
    const vRes = await listAlgorithmVersions(algo.id);
    for (const v of vRes?.data ?? []) {
      allVers.push({ ...v, algoName: `${algo.name} (${v.version})` });
    }
  }
  versions.value = allVers;
}

async function savePolygonROI() {
  if (!draftRoiName.value || draftPoints.value.length < 3) {
    ElMessage.warning("Provide ROI name and at least 3 vertices");
    return;
  }
  await createROI(cameraId, {
    name: draftRoiName.value,
    roi_type: "polygon",
    points: draftPoints.value
  });
  draftRoiName.value = "";
  draftPoints.value = [];
  ElMessage.success("ROI saved");
  await loadAll();
}

async function saveFullFrameROI() {
  await createROI(cameraId, {
    name: draftRoiName.value || "Full Frame",
    roi_type: "full_frame",
    points: []
  });
  draftRoiName.value = "";
  ElMessage.success("Full-frame ROI saved");
  await loadAll();
}

async function removeROI(id: string) {
  await deleteROI(id);
  await loadAll();
}

async function saveRule() {
  const params: Record<string, any> = {
    labels: ruleForm.labels
      .split(",")
      .map(s => s.trim())
      .filter(Boolean),
    min_confidence: ruleForm.min_confidence,
    consecutive_frames: ruleForm.consecutive_frames,
    duration_seconds: ruleForm.duration_seconds,
    cooldown_seconds: ruleForm.cooldown_seconds,
    heartbeat_timeout_seconds: ruleForm.heartbeat_timeout_seconds,
    severity: ruleForm.severity
  };
  if (ruleForm.rule_mode === "count_threshold") {
    params.min_count = ruleForm.min_count;
    params.max_count = ruleForm.max_count;
  }
  if (ruleForm.rule_mode === "absence_detect") {
    params.min_count = ruleForm.min_count;
  }

  await createRule(cameraId, {
    roi_id: ruleForm.roi_id,
    algorithm_version_id: ruleForm.algorithm_version_id,
    name: ruleForm.name,
    rule_mode: ruleForm.rule_mode,
    params,
    schedule_mode: ruleForm.schedule_mode,
    pinned_node_id: ruleForm.pinned_node_id,
    record_video: ruleForm.record_video,
    channel_ids: ruleForm.channel_ids
  });
  ruleDialogVisible.value = false;
  ElMessage.success("Rule created");
  await loadAll();
}

async function removeRule(id: string) {
  await deleteRule(id);
  await loadAll();
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
          editorContainerSize.width = first.contentRect.width;
          editorContainerSize.height = first.contentRect.height;
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
  <div class="p-4 flex flex-col gap-4">
    <el-card shadow="never">
      <div class="font-semibold mb-2">
        {{ camera?.name }} — {{ t("aegis.cameras.roiSection") }}
      </div>
      <div class="text-xs text-zinc-500 mb-2">{{ t("aegis.cameras.roiHint") }}</div>

      <div ref="editorContainerRef" class="relative w-full max-w-[800px] h-[450px] bg-black rounded overflow-hidden">
        <WhepPlayer
          v-if="cameraId"
          :camera-id="cameraId"
          @video-resize="sz => Object.assign(videoSize, sz)"
        />
        <RoiEditor
          v-model="draftPoints"
          :container-width="editorContainerSize.width || 800"
          :container-height="editorContainerSize.height || 450"
          :video-width="videoSize.width"
          :video-height="videoSize.height"
        />
      </div>

      <div class="flex flex-wrap items-center gap-2 mt-3">
        <el-input v-model="draftRoiName" placeholder="ROI Name" class="w-48" size="small" />
        <el-button type="primary" size="small" @click="savePolygonROI">
          {{ t("aegis.cameras.addPolygonRoi") }} ({{ draftPoints.length }})
        </el-button>
        <el-button size="small" @click="saveFullFrameROI">
          {{ t("aegis.cameras.addFullFrameRoi") }}
        </el-button>
        <el-button size="small" @click="draftPoints = []">
          {{ t("aegis.cameras.clearPoints") }}
        </el-button>
      </div>

      <el-table :data="rois" size="small" border class="mt-4">
        <el-table-column prop="name" label="ROI" />
        <el-table-column prop="roi_type" label="Type" width="120" />
        <el-table-column :label="t('aegis.common.actions')" width="100">
          <template #default="{ row }">
            <el-button link type="danger" @click="removeROI(row.id)">
              {{ t("aegis.common.delete") }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-card shadow="never">
      <div class="flex justify-between items-center mb-3">
        <span class="font-semibold">{{ t("aegis.cameras.ruleSection") }}</span>
        <el-button type="primary" size="small" @click="ruleDialogVisible = true">
          {{ t("aegis.cameras.addRule") }}
        </el-button>
      </div>

      <el-table :data="rules" size="small" border>
        <el-table-column prop="name" :label="t('aegis.cameras.ruleName')" />
        <el-table-column prop="rule_mode" :label="t('aegis.alarms.ruleMode')" width="140" />
        <el-table-column prop="schedule_mode" :label="t('aegis.cameras.scheduleMode')" width="110" />
        <el-table-column prop="record_video" :label="t('aegis.cameras.recordVideo')" width="120" />
        <el-table-column :label="t('aegis.common.actions')" width="100">
          <template #default="{ row }">
            <el-button link type="danger" @click="removeRule(row.id)">
              {{ t("aegis.common.delete") }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="ruleDialogVisible" :title="t('aegis.cameras.addRule')" width="560px">
      <el-form label-width="140px" size="small">
        <el-form-item :label="t('aegis.cameras.ruleName')">
          <el-input v-model="ruleForm.name" />
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.roi')">
          <el-select v-model="ruleForm.roi_id" class="w-full">
            <el-option v-for="r in rois" :key="r.id" :label="r.name" :value="r.id" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.algorithmVersion')">
          <el-select v-model="ruleForm.algorithm_version_id" class="w-full">
            <el-option v-for="v in versions" :key="v.id" :label="v.algoName" :value="v.id" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('aegis.alarms.ruleMode')">
          <el-select v-model="ruleForm.rule_mode" class="w-full">
            <el-option label="anomaly_detect" value="anomaly_detect" />
            <el-option label="count_threshold" value="count_threshold" />
            <el-option label="absence_detect" value="absence_detect" />
          </el-select>
        </el-form-item>
        <el-form-item label="Labels">
          <el-input v-model="ruleForm.labels" placeholder="person,no_helmet" />
        </el-form-item>
        <el-form-item label="Duration / Cooldown (s)">
          <div class="flex gap-2">
            <el-input-number v-model="ruleForm.duration_seconds" :min="0" />
            <el-input-number v-model="ruleForm.cooldown_seconds" :min="0" />
          </div>
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
        <el-button type="primary" @click="saveRule">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>
