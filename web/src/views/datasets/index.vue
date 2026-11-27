<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import {
  evidenceImageURL,
  exportFalsePositiveDatasetBlob,
  getZonesTree,
  listFalsePositiveDataset,
  type FalsePositiveSampleItem,
  type ZoneTreeNode
} from "@/api/aegis";
import { formatDateTime } from "@/utils/format";

defineOptions({ name: "AegisDatasets" });

const { t } = useI18n();
const loading = ref(false);
const exporting = ref(false);
const items = ref<FalsePositiveSampleItem[]>([]);
const total = ref(0);
const zones = ref<ZoneTreeNode[]>([]);

const query = reactive({
  zone_id: "",
  false_positive_reason: "",
  dateRange: [] as string[],
  page: 1,
  page_size: 24
});

const reasonOptions = [
  { value: "lighting", labelKey: "aegis.alarms.fpLighting" },
  { value: "occlusion", labelKey: "aegis.alarms.fpOcclusion" },
  { value: "similar_action", labelKey: "aegis.alarms.fpSimilarAction" },
  { value: "model_drift", labelKey: "aegis.alarms.fpModelDrift" },
  { value: "other", labelKey: "aegis.alarms.fpOther" }
];

function reasonLabel(code: string): string {
  const found = reasonOptions.find(o => o.value === code);
  return found ? t(found.labelKey) : code || "—";
}

function buildFilterParams() {
  const params: Record<string, any> = {};
  if (query.zone_id) params.zone_id = query.zone_id;
  if (query.false_positive_reason) params.false_positive_reason = query.false_positive_reason;
  if (query.dateRange?.length === 2) {
    params.start_time = new Date(query.dateRange[0]).toISOString();
    params.end_time = new Date(query.dateRange[1]).toISOString();
  }
  return params;
}

async function loadData() {
  loading.value = true;
  try {
    const res = await listFalsePositiveDataset({
      ...buildFilterParams(),
      page: query.page,
      page_size: query.page_size
    });
    items.value = res?.data?.items ?? [];
    total.value = res?.data?.total ?? 0;
  } catch (err: any) {
    ElMessage.error(err?.message || "Failed to load false-positive samples");
  } finally {
    loading.value = false;
  }
}

async function loadZones() {
  try {
    const res = await getZonesTree();
    zones.value = res?.data ?? [];
  } catch {
    zones.value = [];
  }
}

async function handleExportZip() {
  exporting.value = true;
  try {
    const blob = await exportFalsePositiveDatasetBlob(buildFilterParams());
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `aegis-fp-dataset-${new Date().toISOString().slice(0, 10)}.zip`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 10000);
    ElMessage.success(t("aegis.datasets.exportSuccess"));
  } catch (err: any) {
    ElMessage.error(err?.message || "Failed to export training dataset");
  } finally {
    exporting.value = false;
  }
}

function handleSearch() {
  query.page = 1;
  void loadData();
}

onMounted(() => {
  void loadZones();
  void loadData();
});
</script>

<template>
  <div class="aegis-page">
    <section class="aegis-panel">
      <header class="aegis-page-header">
        <div class="aegis-page-title-group">
          <h1 class="aegis-page-title">{{ t("aegis.datasets.title") }}</h1>
        </div>
        <div class="aegis-header-actions">
          <el-button @click="loadData">{{ t("aegis.common.refresh") }}</el-button>
          <el-button
            type="primary"
            :loading="exporting"
            data-testid="export-dataset-btn"
            @click="handleExportZip"
          >
            {{ t("aegis.datasets.exportZip") }}
          </el-button>
        </div>
      </header>

      <div class="aegis-filter-bar">
        <el-tree-select
          v-model="query.zone_id"
          :data="zones"
          :props="{ label: 'name', value: 'id', children: 'children' }"
          :placeholder="t('aegis.cameras.allZones')"
          clearable
          check-strictly
          style="width: 200px"
          @change="handleSearch"
        />
        <el-select
          v-model="query.false_positive_reason"
          :placeholder="t('aegis.datasets.allReasons')"
          clearable
          style="width: 180px"
          @change="handleSearch"
        >
          <el-option
            v-for="opt in reasonOptions"
            :key="opt.value"
            :label="t(opt.labelKey)"
            :value="opt.value"
          />
        </el-select>
        <el-date-picker
          v-model="query.dateRange"
          type="datetimerange"
          range-separator="-"
          style="width: 320px"
          @change="handleSearch"
        />
      </div>

      <div v-loading="loading">
        <el-empty v-if="items.length === 0" :description="t('aegis.datasets.empty')" />
        <div v-else class="aegis-card-grid">
          <article v-for="sample in items" :key="sample.alarm_id" class="aegis-sample-card">
            <div class="aegis-sample-card__media">
              <img
                :src="evidenceImageURL(sample.alarm_id)"
                :alt="sample.title"
                loading="lazy"
              />
            </div>
            <div class="aegis-sample-card__body">
              <div class="flex items-center justify-between gap-2">
                <span class="font-semibold text-sm truncate" :title="sample.title">
                  {{ sample.title }}
                </span>
                <el-tag size="small" type="warning" class="shrink-0">
                  {{ reasonLabel(sample.false_positive_reason) }}
                </el-tag>
              </div>
              <div class="text-xs text-slate-500 truncate">
                {{ sample.zone_name || "—" }} / {{ sample.camera_name || sample.camera_id }}
              </div>
              <div v-if="sample.algorithm_name" class="text-xs text-slate-500 truncate">
                {{ sample.algorithm_name }} {{ sample.algorithm_version }}
              </div>
              <div
                v-if="sample.handle_note"
                class="text-xs text-slate-600 dark:text-slate-300 truncate"
                :title="sample.handle_note"
              >
                {{ sample.handle_note }}
              </div>
              <div class="mt-auto pt-2 border-t border-[var(--el-border-color-extra-light)] flex items-center justify-between text-[11px] text-slate-500 aegis-tabular">
                <span>{{ t("aegis.datasets.detectionsCount") }}: {{ sample.detections_snapshot?.length ?? 0 }}</span>
                <span>{{ formatDateTime(sample.triggered_at) }}</span>
              </div>
            </div>
          </article>
        </div>

        <div v-if="total > query.page_size" class="aegis-pagination">
          <el-pagination
            v-model:current-page="query.page"
            :page-size="query.page_size"
            :total="total"
            layout="total, prev, pager, next"
            @current-change="loadData"
          />
        </div>
      </div>
    </section>
  </div>
</template>
