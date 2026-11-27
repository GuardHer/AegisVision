<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import * as echarts from "echarts";
import {
  exportAnalyticsCSVBlob,
  getAnalyticsSummary,
  getZonesTree,
  type AnalyticsSummary,
  type ZoneTreeNode
} from "@/api/aegis";

defineOptions({ name: "AegisAnalytics" });

const { t } = useI18n();
const loading = ref(false);
const exporting = ref(false);
const zonesTree = ref<ZoneTreeNode[]>([]);

const filters = reactive({
  rangePreset: "7d" as "today" | "7d" | "30d" | "custom",
  customRange: null as [Date, Date] | null,
  zone_id: "",
  shift: "all" as "all" | "day" | "night",
  granularity: "day" as "day" | "hour"
});

const summary = ref<AnalyticsSummary>({
  kpis: {
    total_alarms: 0,
    safety_alarms: 0,
    sop_alarms: 0,
    confirmed_count: 0,
    false_positive_count: 0,
    false_positive_rate: 0,
    sop_compliant_cycles: 0,
    sop_violated_cycles: 0,
    sop_compliance_rate: 0,
    avg_sop_duration_ms: 0
  },
  trend: [],
  by_zone: [],
  top_sop_steps: [],
  algo_precision: []
});

const flatZones = computed(() => {
  const out: Array<{ id: string; label: string }> = [];
  const walk = (nodes: ZoneTreeNode[], depth: number) => {
    for (const n of nodes) {
      out.push({ id: n.id, label: `${"　".repeat(depth)}${n.name}` });
      if (n.children?.length) walk(n.children, depth + 1);
    }
  };
  walk(zonesTree.value, 0);
  return out;
});

const trendRef = ref<HTMLElement | null>(null);
const zoneRef = ref<HTMLElement | null>(null);
const sopStepsRef = ref<HTMLElement | null>(null);

let trendChart: echarts.ECharts | null = null;
let zoneChart: echarts.ECharts | null = null;
let sopStepsChart: echarts.ECharts | null = null;
let resizeObserver: ResizeObserver | null = null;

function buildQueryParams() {
  const now = new Date();
  let start: Date;
  let end: Date;
  if (filters.rangePreset === "custom" && filters.customRange && filters.customRange.length === 2) {
    start = new Date(filters.customRange[0]);
    end = new Date(filters.customRange[1]);
    end.setDate(end.getDate() + 1);
  } else {
    end = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1);
    const daysBack = filters.rangePreset === "today" ? 0 : filters.rangePreset === "7d" ? 6 : 29;
    start = new Date(now.getFullYear(), now.getMonth(), now.getDate() - daysBack);
  }
  return {
    start_time: start.toISOString(),
    end_time: end.toISOString(),
    zone_id: filters.zone_id || undefined,
    shift: filters.shift,
    granularity: filters.granularity,
    tz: Intl.DateTimeFormat().resolvedOptions().timeZone || "Asia/Shanghai"
  };
}

function renderCharts() {
  if (trendRef.value) {
    if (!trendChart) trendChart = echarts.init(trendRef.value);
    trendChart.setOption({
      tooltip: { trigger: "axis" },
      legend: { bottom: 0 },
      grid: { left: 44, right: 20, top: 18, bottom: 38 },
      xAxis: { type: "category", data: summary.value.trend.map(d => d.bucket) },
      yAxis: { type: "value", minInterval: 1 },
      series: [
        {
          name: t("aegis.dashboard.safetyBehavior"),
          type: "line",
          smooth: true,
          data: summary.value.trend.map(d => d.safety_count),
          itemStyle: { color: "#ef4444" }
        },
        {
          name: t("aegis.dashboard.sopViolation"),
          type: "bar",
          barMaxWidth: 22,
          data: summary.value.trend.map(d => d.sop_count),
          itemStyle: { color: "#f59e0b" }
        }
      ]
    });
  }

  if (zoneRef.value) {
    if (!zoneChart) zoneChart = echarts.init(zoneRef.value);
    const items = [...summary.value.by_zone].reverse();
    zoneChart.setOption({
      tooltip: { trigger: "axis" },
      legend: { bottom: 0 },
      grid: { left: 100, right: 20, top: 14, bottom: 36 },
      xAxis: { type: "value", minInterval: 1 },
      yAxis: { type: "category", data: items.map(z => z.zone_name) },
      series: [
        {
          name: t("aegis.dashboard.safetyBehavior"),
          type: "bar",
          stack: "total",
          barMaxWidth: 16,
          data: items.map(z => z.safety_count),
          itemStyle: { color: "#ef4444" }
        },
        {
          name: t("aegis.dashboard.sopViolation"),
          type: "bar",
          stack: "total",
          barMaxWidth: 16,
          data: items.map(z => z.sop_count),
          itemStyle: { color: "#f59e0b" }
        }
      ]
    });
  }

  if (sopStepsRef.value) {
    if (!sopStepsChart) sopStepsChart = echarts.init(sopStepsRef.value);
    const items = [...summary.value.top_sop_steps].reverse();
    sopStepsChart.setOption({
      tooltip: { trigger: "axis" },
      grid: { left: 120, right: 20, top: 14, bottom: 24 },
      xAxis: { type: "value", minInterval: 1 },
      yAxis: { type: "category", data: items.map(s => `${s.step_name} (${s.violation_type})`) },
      series: [
        {
          type: "bar",
          barMaxWidth: 16,
          data: items.map(s => s.count),
          itemStyle: { color: "#6366f1", borderRadius: [0, 4, 4, 0] }
        }
      ]
    });
  }
}

async function loadData() {
  loading.value = true;
  try {
    const res = await getAnalyticsSummary(buildQueryParams());
    if (res?.data) {
      summary.value = res.data;
    }
    await nextTick();
    renderCharts();
  } catch (err: any) {
    ElMessage.error(err?.response?.data?.message || err?.message || "Failed to load analytics");
  } finally {
    loading.value = false;
  }
}

async function onExportCSV() {
  exporting.value = true;
  try {
    const blob = await exportAnalyticsCSVBlob(buildQueryParams());
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `aegis-analytics-${Date.now()}.csv`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 10000);
  } catch (err: any) {
    ElMessage.error(err?.message || "Export failed");
  } finally {
    exporting.value = false;
  }
}

onMounted(async () => {
  const zRes = await getZonesTree().catch(() => ({ data: [] as ZoneTreeNode[] }));
  zonesTree.value = zRes?.data ?? [];
  await loadData();
  if (typeof ResizeObserver !== "undefined") {
    resizeObserver = new ResizeObserver(() => {
      trendChart?.resize();
      zoneChart?.resize();
      sopStepsChart?.resize();
    });
    if (trendRef.value) resizeObserver.observe(trendRef.value);
    if (zoneRef.value) resizeObserver.observe(zoneRef.value);
    if (sopStepsRef.value) resizeObserver.observe(sopStepsRef.value);
  }
});

onBeforeUnmount(() => {
  resizeObserver?.disconnect();
  trendChart?.dispose();
  zoneChart?.dispose();
  sopStepsChart?.dispose();
});
</script>

<template>
  <div class="aegis-page">
    <!-- Header & Filter Toolbar -->
    <section class="aegis-panel">
      <header class="aegis-page-header">
        <div class="aegis-page-title-group">
          <h1 class="aegis-page-title">{{ t("aegis.analytics.title") }}</h1>
        </div>
        <div class="aegis-header-actions">
          <el-button size="small" :loading="loading" @click="loadData">{{ t("aegis.common.refresh") }}</el-button>
          <el-button size="small" type="primary" :loading="exporting" data-test="export-analytics-btn" @click="onExportCSV">
            {{ t("aegis.analytics.exportCsv") }}
          </el-button>
        </div>
      </header>

      <div class="aegis-filter-bar !mb-0">
        <el-radio-group v-model="filters.rangePreset" size="small" @change="loadData">
          <el-radio-button value="today">{{ t("aegis.analytics.today") }}</el-radio-button>
          <el-radio-button value="7d">{{ t("aegis.analytics.last7d") }}</el-radio-button>
          <el-radio-button value="30d">{{ t("aegis.analytics.last30d") }}</el-radio-button>
          <el-radio-button value="custom">Custom</el-radio-button>
        </el-radio-group>
        <el-date-picker
          v-if="filters.rangePreset === 'custom'"
          v-model="filters.customRange"
          type="daterange"
          size="small"
          style="width: 224px"
          @change="loadData"
        />
        <el-select v-model="filters.zone_id" clearable size="small" style="width: 175px" :placeholder="t('aegis.alarms.zone')" @change="loadData">
          <el-option v-for="z in flatZones" :key="z.id" :label="z.label" :value="z.id" />
        </el-select>
        <el-select v-model="filters.shift" size="small" style="width: 165px" @change="loadData">
          <el-option :label="t('aegis.analytics.shiftAll')" value="all" />
          <el-option :label="t('aegis.analytics.shiftDay')" value="day" />
          <el-option :label="t('aegis.analytics.shiftNight')" value="night" />
        </el-select>
        <el-radio-group v-model="filters.granularity" size="small" @change="loadData">
          <el-radio-button value="day">{{ t("aegis.analytics.byDay") }}</el-radio-button>
          <el-radio-button value="hour">{{ t("aegis.analytics.byHour") }}</el-radio-button>
        </el-radio-group>
      </div>
    </section>

    <!-- 4 Period Summary KPI Cards -->
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3 aegis-tabular">
      <el-card shadow="hover">
        <div class="text-xs text-zinc-500">{{ t("aegis.analytics.totalAlarms") }}</div>
        <div class="text-2xl font-bold mt-1" data-test="an-total-alarms">{{ summary.kpis.total_alarms }}</div>
        <div class="text-xs text-zinc-400 mt-1">
          Safety: {{ summary.kpis.safety_alarms }} · SOP: {{ summary.kpis.sop_alarms }}
        </div>
      </el-card>

      <el-card shadow="hover">
        <div class="text-xs text-zinc-500">{{ t("aegis.analytics.sopComplianceRate") }}</div>
        <div class="text-2xl font-bold text-blue-600 mt-1" data-test="an-sop-compliance">
          {{ summary.kpis.sop_compliance_rate }}%
        </div>
        <div class="text-xs text-zinc-400 mt-1">
          OK: {{ summary.kpis.sop_compliant_cycles }} / Violated: {{ summary.kpis.sop_violated_cycles }}
        </div>
      </el-card>

      <el-card shadow="hover">
        <div class="text-xs text-zinc-500">{{ t("aegis.analytics.avgSopDuration") }}</div>
        <div class="text-2xl font-bold mt-1" data-test="an-avg-duration">
          {{ (summary.kpis.avg_sop_duration_ms / 1000).toFixed(1) }}s
        </div>
      </el-card>

      <el-card shadow="hover">
        <div class="text-xs text-zinc-500">{{ t("aegis.analytics.fpRate") }}</div>
        <div class="text-2xl font-bold text-amber-500 mt-1" data-test="an-fp-rate">
          {{ summary.kpis.false_positive_rate }}%
        </div>
        <div class="text-xs text-zinc-400 mt-1">
          Confirmed: {{ summary.kpis.confirmed_count }} / FP: {{ summary.kpis.false_positive_count }}
        </div>
      </el-card>
    </div>

    <!-- Trend & Zone Breakdown -->
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <el-card shadow="never">
        <template #header><span class="font-medium text-sm">{{ t("aegis.analytics.trendChart") }}</span></template>
        <div ref="trendRef" class="w-full h-72" />
      </el-card>

      <el-card shadow="never">
        <template #header><span class="font-medium text-sm">{{ t("aegis.analytics.zoneChart") }}</span></template>
        <div ref="zoneRef" class="w-full h-72" />
      </el-card>
    </div>

    <!-- Top SOP Violated Steps & Algorithm Precision Table -->
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <el-card shadow="never">
        <template #header><span class="font-medium text-sm">{{ t("aegis.analytics.topSopSteps") }}</span></template>
        <div ref="sopStepsRef" class="w-full h-72" />
      </el-card>

      <el-card shadow="never">
        <template #header><span class="font-medium text-sm">{{ t("aegis.analytics.algoPrecision") }}</span></template>
        <div class="aegis-table-wrap" tabindex="0" role="region" :aria-label="t('aegis.analytics.algoPrecision')">
          <el-table :data="summary.algo_precision" size="small" border stripe max-height="288">
            <el-table-column prop="algorithm_name" :label="t('aegis.algorithms.name')" min-width="130" show-overflow-tooltip />
            <el-table-column prop="version" :label="t('aegis.algorithms.version')" width="85" />
            <el-table-column prop="total_alarms" label="Total" width="72" align="right" />
            <el-table-column prop="confirmed_count" label="Confirmed" width="88" align="right" />
            <el-table-column prop="false_positive_count" label="FP" width="60" align="right" />
            <el-table-column label="FP Rate" width="86">
              <template #default="{ row }">
                <el-tag :type="row.false_positive_rate > 30 ? 'danger' : 'success'" size="small">
                  {{ row.false_positive_rate }}%
                </el-tag>
              </template>
            </el-table-column>
          </el-table>
        </div>
      </el-card>
    </div>
  </div>
</template>
