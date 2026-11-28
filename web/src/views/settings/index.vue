<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import {
  getPlatformSettings,
  getSystemHealth,
  updatePlatformSettings,
  type PlatformSettings,
  type SystemHealthPayload
} from "@/api/aegis";
import { showError, showSuccess } from "@/utils/error";
import {
  formatHealthComponent,
  formatHealthStatus,
  formatSeverity
} from "@/utils/aegisLabels";

defineOptions({ name: "AegisSettings" });

const { t } = useI18n();
const loadingSettings = ref(false);
const settingsError = ref("");
const savingSettings = ref(false);
const loadingHealth = ref(false);
const healthError = ref("");
const health = ref<SystemHealthPayload | null>(null);
const hasDegradedComponents = computed(() =>
  (health.value?.components ?? []).some(c => c.status === "degraded")
);

const form = reactive<PlatformSettings>({
  platform_title: "AegisVision EHS 智能视频监控平台",
  default_alarm_chime: true,
  popup_min_severity: "warning",
  max_export_rows: 500,
  default_timezone: "Asia/Shanghai"
});

const commonTimezones = [
  "Asia/Shanghai",
  "Asia/Tokyo",
  "Asia/Singapore",
  "UTC",
  "Europe/Berlin",
  "America/New_York"
];

async function loadSettings() {
  loadingSettings.value = true;
  try {
    const res = await getPlatformSettings();
    if (res?.data) {
      Object.assign(form, res.data);
    }
    settingsError.value = "";
  } catch (err: any) {
    settingsError.value = showError(err, t("aegis.common.loadFailed"));
  } finally {
    loadingSettings.value = false;
  }
}

async function loadHealth() {
  loadingHealth.value = true;
  try {
    const res = await getSystemHealth();
    health.value = res?.data ?? null;
    healthError.value = "";
  } catch (err: any) {
    healthError.value = showError(err, t("aegis.common.loadFailed"));
  } finally {
    loadingHealth.value = false;
  }
}

async function saveSettings() {
  savingSettings.value = true;
  try {
    const res = await updatePlatformSettings({ ...form });
    if (res?.data) {
      Object.assign(form, res.data);
    }
    showSuccess(t("aegis.settings.saved"));
  } catch (err: any) {
    showError(err);
  } finally {
    savingSettings.value = false;
  }
}

function healthTagType(status: string): "success" | "danger" | "info" {
  if (status === "healthy") return "success";
  if (status === "degraded") return "danger";
  return "info";
}

onMounted(() => {
  void loadSettings();
  void loadHealth();
});
</script>

<template>
  <div class="aegis-page">
    <div class="settings-grid">
      <section v-loading="loadingSettings" class="aegis-panel">
        <header class="aegis-page-header">
          <div class="aegis-page-title-group">
            <h1 class="aegis-page-title">{{ t("aegis.settings.platformConfig") }}</h1>
          </div>
        </header>

        <el-alert
          v-if="settingsError"
          type="error"
          show-icon
          closable
          :title="t('aegis.common.loadFailed')"
          class="mb-3"
          @close="settingsError = ''"
        >
          <template #default>
            <div class="flex flex-wrap items-center justify-between gap-2">
              <span>{{ settingsError }}</span>
              <el-button size="small" type="danger" plain @click="loadSettings">
                {{ t("aegis.common.retry") }}
              </el-button>
            </div>
          </template>
        </el-alert>

        <el-form label-width="190px" size="default">
          <el-form-item :label="t('aegis.settings.platformTitle')">
            <el-input v-model="form.platform_title" />
          </el-form-item>
          <el-form-item :label="t('aegis.settings.defaultAlarmChime')">
            <el-switch v-model="form.default_alarm_chime" />
          </el-form-item>
          <el-form-item :label="t('aegis.settings.popupMinSeverity')">
            <el-select v-model="form.popup_min_severity" class="w-full">
              <el-option :label="formatSeverity('info', t)" value="info" />
              <el-option :label="formatSeverity('warning', t)" value="warning" />
              <el-option :label="formatSeverity('critical', t)" value="critical" />
            </el-select>
          </el-form-item>
          <el-form-item :label="t('aegis.settings.maxExportRows')">
            <el-input-number v-model="form.max_export_rows" :min="10" :max="2000" :step="50" />
          </el-form-item>
          <el-form-item :label="t('aegis.settings.defaultTimezone')">
            <el-select v-model="form.default_timezone" filterable allow-create class="w-full">
              <el-option v-for="tz in commonTimezones" :key="tz" :label="tz" :value="tz" />
            </el-select>
          </el-form-item>
          <el-form-item>
            <el-button
              type="primary"
              :loading="savingSettings"
              data-testid="save-settings-btn"
              @click="saveSettings"
            >
              {{ t("aegis.common.save") }}
            </el-button>
          </el-form-item>
        </el-form>
      </section>

      <section v-loading="loadingHealth" class="aegis-panel">
        <header class="aegis-page-header">
          <div class="aegis-page-title-group">
            <h2 class="aegis-page-title">{{ t("aegis.settings.systemHealth") }}</h2>
          </div>
          <div class="aegis-header-actions">
            <el-button size="small" @click="loadHealth">{{ t("aegis.common.refresh") }}</el-button>
          </div>
        </header>

        <el-alert
          v-if="healthError"
          type="error"
          show-icon
          closable
          :title="t('aegis.common.loadFailed')"
          class="mb-3"
          @close="healthError = ''"
        >
          <template #default>
            <div class="flex flex-wrap items-center justify-between gap-2">
              <span>{{ healthError }}</span>
              <el-button size="small" type="danger" plain @click="loadHealth">
                {{ t("aegis.common.retry") }}
              </el-button>
            </div>
          </template>
        </el-alert>

        <div v-if="health" class="space-y-4">
          <el-alert
            :type="hasDegradedComponents ? 'warning' : 'success'"
            show-icon
            :closable="false"
            :title="hasDegradedComponents ? t('aegis.settings.healthDegradedWarning') : t('aegis.settings.healthAllHealthy')"
          />
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div
              v-for="comp in health.components"
              :key="comp.name"
              class="border border-[var(--el-border-color-lighter)] rounded-md p-3 flex items-center justify-between gap-2"
            >
              <div class="min-w-0">
                <div class="font-medium text-sm truncate">{{ formatHealthComponent(comp.name, t) }}</div>
                <div class="text-xs text-slate-400 truncate aegis-tabular">
                  {{ comp.detail || "—" }} · {{ comp.latency_ms }}ms
                </div>
              </div>
              <el-tag size="small" :type="healthTagType(comp.status)" class="shrink-0">
                {{ formatHealthStatus(comp.status, t) }}
              </el-tag>
            </div>
          </div>

          <el-divider content-position="left">{{ t("aegis.settings.clusterSummary") }}</el-divider>
          <div class="grid grid-cols-3 gap-3 text-center">
            <div class="border border-[var(--el-border-color-lighter)] rounded-md p-3">
              <div class="text-xs text-slate-400">{{ t("aegis.dashboard.camerasOnline") }}</div>
              <div class="text-xl font-bold mt-1 aegis-tabular">
                {{ health.cluster.cameras_online }} / {{ health.cluster.cameras_total }}
              </div>
            </div>
            <div class="border border-[var(--el-border-color-lighter)] rounded-md p-3">
              <div class="text-xs text-slate-400">{{ t("aegis.nodes.nodesTab") }}</div>
              <div class="text-xl font-bold mt-1 aegis-tabular">
                {{ health.cluster.nodes_online }} / {{ health.cluster.nodes_total }}
              </div>
            </div>
            <div class="border border-[var(--el-border-color-lighter)] rounded-md p-3">
              <div class="text-xs text-slate-400">{{ t("aegis.nodes.tasksTab") }}</div>
              <div class="text-xl font-bold mt-1 aegis-tabular">
                {{ health.cluster.running_tasks }}
              </div>
            </div>
          </div>
        </div>
        <el-empty v-else-if="!loadingHealth" :image-size="60" :description="t('aegis.settings.healthEmpty')" />
      </section>
    </div>
  </div>
</template>

<style scoped lang="scss">
.settings-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 420px), 1fr));
  gap: 14px;
  align-items: start;
}
</style>
