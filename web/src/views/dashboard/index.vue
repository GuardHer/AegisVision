<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import * as echarts from "echarts";
import {
  getDashboardOverview,
  handleAlarm,
  type DashboardOverview
} from "@/api/aegis";
import { useUserStoreHook } from "@/store/modules/user";
import { canHandleAlarms } from "@/utils/permissions";

defineOptions({ name: "AegisDashboard" });

const { t } = useI18n();
const router = useRouter();
const userStore = useUserStoreHook();
const allowHandle = computed(() =>
  (userStore.roles ?? []).some(r => canHandleAlarms(r))
);

const loading = ref(false);
const overview = ref<DashboardOverview>({
  kpis: {
    today_alarms: 0,
    yesterday_alarms: 0,
    unhandled_alarms: 0,
    confirmed_rate: 0,
    false_positive_rate: 0,
    today_sop_violations: 0,
    sop_compliance_rate: 0,
    cameras_online: 0,
    cameras_total: 0,
    nodes_online: 0,
    nodes_total: 0,
    running_tasks: 0
  },
  trend_7d: [],
  top_zones: [],
  by_rule_mode: [],
  recent_alerts: []
});

const trendChartRef = ref<HTMLElement | null>(null);
const zonesChartRef = ref<HTMLElement | null>(null);
const modeChartRef = ref<HTMLElement | null>(null);

let trendChart: echarts.ECharts | null = null;
let zonesChart: echarts.ECharts | null = null;
let modeChart: echarts.ECharts | null = null;
let resizeObserver: ResizeObserver | null = null;
let refreshTimer: ReturnType<typeof setInterval> | null = null;

const deltaToday = computed(() => {
  const diff = overview.value.kpis.today_alarms - overview.value.kpis.yesterday_alarms;
  return diff >= 0 ? `+${diff}` : `${diff}`;
});

function renderCharts() {
  if (trendChartRef.value) {
    if (!trendChart) trendChart = echarts.init(trendChartRef.value);
    trendChart.setOption({
      tooltip: { trigger: "axis" },
      legend: { bottom: 0 },
      grid: { left: 40, right: 20, top: 20, bottom: 38 },
      xAxis: {
        type: "category",
        data: overview.value.trend_7d.map(d => d.date.slice(5))
      },
      yAxis: { type: "value", minInterval: 1 },
      series: [
        {
          name: t("aegis.dashboard.safetyBehavior"),
          type: "line",
          smooth: true,
          data: overview.value.trend_7d.map(d => d.safety_count),
          itemStyle: { color: "#ef4444" }
        },
        {
          name: t("aegis.dashboard.sopViolation"),
          type: "bar",
          barMaxWidth: 22,
          data: overview.value.trend_7d.map(d => d.sop_count),
          itemStyle: { color: "#f59e0b" }
        }
      ]
    });
  }

  if (zonesChartRef.value) {
    if (!zonesChart) zonesChart = echarts.init(zonesChartRef.value);
    const items = [...overview.value.top_zones].reverse();
    zonesChart.setOption({
      tooltip: { trigger: "axis" },
      grid: { left: 100, right: 24, top: 12, bottom: 24 },
      xAxis: { type: "value", minInterval: 1 },
      yAxis: { type: "category", data: items.map(z => z.zone_name) },
      series: [
        {
          type: "bar",
          barMaxWidth: 18,
          data: items.map(z => z.alarm_count),
          itemStyle: { color: "#3b82f6", borderRadius: [0, 4, 4, 0] }
        }
      ]
    });
  }

  if (modeChartRef.value) {
    if (!modeChart) modeChart = echarts.init(modeChartRef.value);
    modeChart.setOption({
      tooltip: { trigger: "item" },
      legend: { bottom: 0 },
      series: [
        {
          type: "pie",
          radius: ["45%", "72%"],
          center: ["50%", "44%"],
          label: { show: false },
          data: overview.value.by_rule_mode.map(m => ({
            name: m.rule_mode,
            value: m.count
          }))
        }
      ]
    });
  }
}

async function loadData() {
  loading.value = true;
  try {
    const res = await getDashboardOverview();
    if (res?.data) {
      overview.value = res.data;
    }
    await nextTick();
    renderCharts();
  } finally {
    loading.value = false;
  }
}

async function quickHandle(alarmId: string, status: "confirmed" | "false_positive") {
  await handleAlarm(alarmId, {
    handle_status: status,
    handle_note: "Dashboard quick action"
  });
  ElMessage.success("OK");
  await loadData();
}

onMounted(() => {
  void loadData();
  if (typeof ResizeObserver !== "undefined") {
    resizeObserver = new ResizeObserver(() => {
      trendChart?.resize();
      zonesChart?.resize();
      modeChart?.resize();
    });
    if (trendChartRef.value) resizeObserver.observe(trendChartRef.value);
    if (zonesChartRef.value) resizeObserver.observe(zonesChartRef.value);
    if (modeChartRef.value) resizeObserver.observe(modeChartRef.value);
  }
  refreshTimer = setInterval(() => void loadData(), 30000);
});

onBeforeUnmount(() => {
  if (refreshTimer) clearInterval(refreshTimer);
  resizeObserver?.disconnect();
  trendChart?.dispose();
  zonesChart?.dispose();
  modeChart?.dispose();
});
</script>

<template>
  <div class="p-4 flex flex-col gap-4">
    <div class="flex items-center justify-between">
      <div class="text-lg font-semibold">{{ t("aegis.dashboard.title") }}</div>
      <div class="flex gap-2">
        <el-button size="small" @click="router.push('/monitor/index')">
          {{ t("menus.aegisMonitor") }}
        </el-button>
        <el-button size="small" type="primary" :loading="loading" @click="loadData">
          {{ t("aegis.common.refresh") }}
        </el-button>
      </div>
    </div>

    <!-- 6 Enterprise KPI Cards -->
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-6 gap-3">
      <el-card shadow="hover">
        <div class="text-xs text-zinc-500">{{ t("aegis.dashboard.todayAlarms") }}</div>
        <div class="text-2xl font-bold mt-1" data-test="kpi-today-alarms">
          {{ overview.kpis.today_alarms }}
        </div>
        <div class="text-xs text-zinc-400 mt-1">
          {{ t("aegis.dashboard.vsYesterday") }}: {{ deltaToday }}
        </div>
      </el-card>

      <el-card
        shadow="hover"
        class="cursor-pointer border-red-200"
        data-test="kpi-unhandled-card"
        @click="router.push({ path: '/alarms/index', query: { handle_status: 'unhandled' } })"
      >
        <div class="text-xs text-zinc-500">{{ t("aegis.dashboard.unhandledAlarms") }}</div>
        <div class="text-2xl font-bold text-red-500 mt-1" data-test="kpi-unhandled-alarms">
          {{ overview.kpis.unhandled_alarms }}
        </div>
        <div class="text-xs text-red-400 mt-1">{{ t("aegis.dashboard.viewAll") }} →</div>
      </el-card>

      <el-card shadow="hover">
        <div class="text-xs text-zinc-500">{{ t("aegis.dashboard.confirmedAndFp") }}</div>
        <div class="text-xl font-bold mt-1">
          <span class="text-emerald-600">{{ overview.kpis.confirmed_rate }}%</span>
          <span class="text-zinc-300 mx-1">/</span>
          <span class="text-amber-500">{{ overview.kpis.false_positive_rate }}%</span>
        </div>
      </el-card>

      <el-card shadow="hover">
        <div class="text-xs text-zinc-500">{{ t("aegis.dashboard.sopCompliance") }}</div>
        <div class="text-2xl font-bold text-blue-600 mt-1" data-test="kpi-sop-compliance">
          {{ overview.kpis.sop_compliance_rate }}%
        </div>
        <div class="text-xs text-zinc-400 mt-1">
          {{ t("aegis.dashboard.sopViolationsToday") }}: {{ overview.kpis.today_sop_violations }}
        </div>
      </el-card>

      <el-card shadow="hover">
        <div class="text-xs text-zinc-500">{{ t("aegis.dashboard.camerasOnline") }}</div>
        <div class="text-2xl font-bold mt-1">
          {{ overview.kpis.cameras_online }} / {{ overview.kpis.cameras_total }}
        </div>
      </el-card>

      <el-card shadow="hover">
        <div class="text-xs text-zinc-500">{{ t("aegis.dashboard.computeCluster") }}</div>
        <div class="text-2xl font-bold mt-1">
          {{ overview.kpis.nodes_online }}/{{ overview.kpis.nodes_total }}
        </div>
        <div class="text-xs text-zinc-400 mt-1">
          Tasks: {{ overview.kpis.running_tasks }}
        </div>
      </el-card>
    </div>

    <!-- Charts Row -->
    <div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
      <el-card shadow="never" class="lg:col-span-2">
        <template #header>
          <span class="font-medium text-sm">{{ t("aegis.dashboard.trend7d") }}</span>
        </template>
        <div ref="trendChartRef" class="w-full h-72" />
      </el-card>

      <el-card shadow="never">
        <template #header>
          <span class="font-medium text-sm">{{ t("aegis.dashboard.behaviorDist") }}</span>
        </template>
        <div ref="modeChartRef" class="w-full h-72" />
      </el-card>
    </div>

    <!-- Bottom Row: Top Zones & Recent Critical/Warning Alerts -->
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <el-card shadow="never">
        <template #header>
          <span class="font-medium text-sm">{{ t("aegis.dashboard.topZones") }}</span>
        </template>
        <div ref="zonesChartRef" class="w-full h-72" />
      </el-card>

      <el-card shadow="never">
        <template #header>
          <div class="flex items-center justify-between">
            <span class="font-medium text-sm">{{ t("aegis.dashboard.recentAlerts") }}</span>
            <el-button link type="primary" size="small" @click="router.push('/alarms/index')">
              {{ t("aegis.dashboard.viewAll") }}
            </el-button>
          </div>
        </template>

        <el-empty
          v-if="overview.recent_alerts.length === 0"
          :description="t('aegis.dashboard.noRecentAlerts')"
          :image-size="60"
        />
        <div v-else class="flex flex-col gap-2 max-h-72 overflow-y-auto">
          <div
            v-for="alert in overview.recent_alerts"
            :key="alert.id"
            class="flex items-center justify-between gap-2 p-2.5 rounded border text-xs"
          >
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2 font-medium truncate">
                <el-tag :type="alert.severity === 'critical' ? 'danger' : 'warning'" size="small">
                  {{ alert.severity }}
                </el-tag>
                <span class="truncate">{{ alert.title }}</span>
              </div>
              <div class="text-zinc-500 mt-1">
                {{ alert.zone_name || "-" }} · {{ alert.camera_name }} · {{ alert.triggered_at }}
              </div>
            </div>
            <div v-if="allowHandle" class="flex gap-1 shrink-0">
              <el-button size="small" type="success" plain @click="quickHandle(alert.id, 'confirmed')">
                {{ t("aegis.alarms.confirm") }}
              </el-button>
              <el-button size="small" type="warning" plain @click="quickHandle(alert.id, 'false_positive')">
                {{ t("aegis.alarms.falsePositive") }}
              </el-button>
            </div>
          </div>
        </div>
      </el-card>
    </div>
  </div>
</template>
