<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import {
  evidenceImageURL,
  evidenceVideoURL,
  handleAlarm,
  listAlarmDeliveries,
  listAlarms,
  listCameras,
  retryDelivery,
  type AlarmDeliveryItem,
  type AlarmItem,
  type CameraItem
} from "@/api/aegis";
import { useUserStoreHook } from "@/store/modules/user";
import { canHandleAlarms } from "@/utils/permissions";
import CanvasOverlay from "@/components/aegis/CanvasOverlay.vue";

defineOptions({ name: "AegisAlarms" });

const { t } = useI18n();
const userStore = useUserStoreHook();
const allowHandle = computed(() =>
  (userStore.roles ?? []).some(r => canHandleAlarms(r))
);

const loading = ref(false);
const cameras = ref<CameraItem[]>([]);
const alarms = ref<AlarmItem[]>([]);
const total = ref(0);
const query = reactive({
  page: 1,
  page_size: 20,
  camera_id: "",
  rule_mode: "",
  handle_status: ""
});

const drawerVisible = ref(false);
const selectedAlarm = ref<AlarmItem | null>(null);
const deliveries = ref<AlarmDeliveryItem[]>([]);
const snapshotContainerRef = ref<HTMLElement | null>(null);
const snapshotContainerSize = reactive({ width: 640, height: 288 });
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
  handle_note: ""
});

async function loadData() {
  loading.value = true;
  try {
    const res = await listAlarms({ ...query });
    alarms.value = res?.data?.items ?? [];
    total.value = res?.data?.total ?? 0;
  } finally {
    loading.value = false;
  }
}

async function openDetail(row: AlarmItem) {
  selectedAlarm.value = row;
  handleForm.handle_status =
    row.handle_status === "unhandled" ? "confirmed" : row.handle_status;
  handleForm.handle_note = row.handle_note || "";
  drawerVisible.value = true;
  const delRes = await listAlarmDeliveries(row.id);
  deliveries.value = delRes?.data ?? [];
}

async function submitHandle() {
  if (!selectedAlarm.value) return;
  const res = await handleAlarm(selectedAlarm.value.id, { ...handleForm });
  if (res?.code === 0) {
    ElMessage.success("OK");
    selectedAlarm.value = res.data;
    await loadData();
  }
}

async function onRetryDelivery(deliveryId: string) {
  await retryDelivery(deliveryId);
  ElMessage.success("Retry enqueued");
  if (selectedAlarm.value) {
    const delRes = await listAlarmDeliveries(selectedAlarm.value.id);
    deliveries.value = delRes?.data ?? [];
  }
}

onMounted(async () => {
  const camRes = await listCameras();
  cameras.value = camRes?.data ?? [];
  await loadData();
});
</script>

<template>
  <div class="p-4 flex flex-col gap-4">
    <el-card shadow="never">
      <div class="flex flex-wrap items-center gap-3 mb-4">
        <el-select v-model="query.camera_id" clearable :placeholder="t('aegis.alarms.camera')" class="w-48">
          <el-option v-for="c in cameras" :key="c.id" :label="c.name" :value="c.id" />
        </el-select>
        <el-select v-model="query.rule_mode" clearable :placeholder="t('aegis.alarms.ruleMode')" class="w-44">
          <el-option label="anomaly_detect" value="anomaly_detect" />
          <el-option label="count_threshold" value="count_threshold" />
          <el-option label="absence_detect" value="absence_detect" />
        </el-select>
        <el-select v-model="query.handle_status" clearable :placeholder="t('aegis.alarms.handleStatus')" class="w-44">
          <el-option label="unhandled" value="unhandled" />
          <el-option label="confirmed" value="confirmed" />
          <el-option label="false_positive" value="false_positive" />
        </el-select>
        <el-button type="primary" @click="query.page = 1; loadData()">
          {{ t("aegis.common.refresh") }}
        </el-button>
      </div>

      <el-table v-loading="loading" :data="alarms" border stripe>
        <el-table-column prop="title" :label="t('aegis.alarms.title')" min-width="200" />
        <el-table-column prop="rule_mode" :label="t('aegis.alarms.ruleMode')" width="140" />
        <el-table-column prop="severity" :label="t('aegis.alarms.severity')" width="100">
          <template #default="{ row }">
            <el-tag :type="row.severity === 'critical' ? 'danger' : 'warning'" size="small">
              {{ row.severity }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="handle_status" :label="t('aegis.alarms.handleStatus')" width="130" />
        <el-table-column prop="video_status" :label="t('aegis.alarms.videoStatus')" width="120" />
        <el-table-column prop="triggered_at" :label="t('aegis.alarms.triggeredAt')" width="200" />
        <el-table-column :label="t('aegis.common.actions')" width="120" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row)">
              {{ t("aegis.alarms.detail") }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="mt-4 flex justify-end">
        <el-pagination
          v-model:current-page="query.page"
          v-model:page-size="query.page_size"
          :total="total"
          layout="total, prev, pager, next"
          @current-change="loadData"
        />
      </div>
    </el-card>

    <el-drawer v-model="drawerVisible" :title="t('aegis.alarms.detail')" size="50%">
      <template v-if="selectedAlarm">
        <div class="flex flex-col gap-4">
          <div class="font-semibold">{{ selectedAlarm.title }}</div>

          <div v-if="selectedAlarm.image_object_key">
            <div class="text-xs text-zinc-500 mb-1">{{ t("aegis.alarms.snapshot") }}</div>
            <div ref="snapshotContainerRef" class="relative w-full h-72 bg-black rounded overflow-hidden">
              <img
                :src="evidenceImageURL(selectedAlarm.id)"
                class="w-full h-full object-contain"
                @load="onSnapshotLoad"
              />
              <CanvasOverlay
                :container-width="snapshotContainerSize.width || 640"
                :container-height="snapshotContainerSize.height || 288"
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
            <div class="text-xs text-zinc-500 mb-1">{{ t("aegis.alarms.videoClip") }}</div>
            <video
              controls
              class="w-full max-h-72 bg-black rounded"
              :src="evidenceVideoURL(selectedAlarm.id)"
            />
          </div>

          <div v-if="allowHandle" class="border rounded p-3">
            <div class="font-medium text-sm mb-2">{{ t("aegis.alarms.handle") }}</div>
            <el-form label-width="100px" size="small">
              <el-form-item :label="t('aegis.alarms.handleStatus')">
                <el-radio-group v-model="handleForm.handle_status">
                  <el-radio value="confirmed">{{ t("aegis.alarms.confirm") }}</el-radio>
                  <el-radio value="false_positive">{{ t("aegis.alarms.falsePositive") }}</el-radio>
                </el-radio-group>
              </el-form-item>
              <el-form-item :label="t('aegis.alarms.handleNote')">
                <el-input v-model="handleForm.handle_note" type="textarea" :rows="2" />
              </el-form-item>
              <el-form-item>
                <el-button type="primary" @click="submitHandle">
                  {{ t("aegis.common.save") }}
                </el-button>
              </el-form-item>
            </el-form>
          </div>

          <div>
            <div class="font-medium text-sm mb-2">{{ t("aegis.alarms.deliveries") }}</div>
            <el-table :data="deliveries" size="small" border>
              <el-table-column prop="channel_type" label="Type" width="90" />
              <el-table-column prop="status" :label="t('aegis.common.status')" width="100" />
              <el-table-column prop="attempts" label="Attempts" width="85" />
              <el-table-column prop="last_status_code" label="HTTP" width="75" />
              <el-table-column prop="last_error" label="Error" min-width="140" />
              <el-table-column v-if="allowHandle" :label="t('aegis.common.actions')" width="110">
                <template #default="{ row }">
                  <el-button
                    v-if="row.status !== 'success'"
                    link
                    type="primary"
                    @click="onRetryDelivery(row.id)"
                  >
                    {{ t("aegis.alarms.retry") }}
                  </el-button>
                </template>
              </el-table-column>
            </el-table>
          </div>
        </div>
      </template>
    </el-drawer>
  </div>
</template>
