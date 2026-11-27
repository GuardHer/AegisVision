<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import {
  getPlatformSettings,
  getSystemHealth,
  updatePlatformSettings,
  type PlatformSettings,
  type SystemHealthPayload
} from "@/api/aegis";

defineOptions({ name: "AegisSettings" });

const { t } = useI18n();
const loadingSettings = ref(false);
const savingSettings = ref(false);
const loadingHealth = ref(false);
const health = ref<SystemHealthPayload | null>(null);

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
  } catch (err: any) {
    ElMessage.error(err?.message || "Failed to load settings");
  } finally {
    loadingSettings.value = false;
  }
}

async function loadHealth() {
  loadingHealth.value = true;
  try {
    const res = await getSystemHealth();
    health.value = res?.data ?? null;
  } catch (err: any) {
    ElMessage.error(err?.message || "Failed to load system health");
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
    ElMessage.success(t("aegis.settings.saved"));
  } catch (err: any) {
    ElMessage.error(err?.message || "Failed to save settings");
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
  loadSettings();
  loadHealth();
});
</script>

<template>
  <div class="p-4 space-y-4">
    <el-row :gutter="16">
      <el-col :xs="24" :lg="12">
        <el-card shadow="never" v-loading="loadingSettings">
          <template #header>
            <div class="font-semibold">{{ t("aegis.settings.platformConfig") }}</div>
          </template>
          <el-form label-width="190px">
            <el-form-item :label="t('aegis.settings.platformTitle')">
              <el-input v-model="form.platform_title" />
            </el-form-item>
            <el-form-item :label="t('aegis.settings.defaultAlarmChime')">
              <el-switch v-model="form.default_alarm_chime" />
            </el-form-item>
            <el-form-item :label="t('aegis.settings.popupMinSeverity')">
              <el-select v-model="form.popup_min_severity" class="w-full">
                <el-option label="info" value="info" />
                <el-option label="warning" value="warning" />
                <el-option label="critical" value="critical" />
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
        </el-card>
      </el-col>

      <el-col :xs="24" :lg="12">
        <el-card shadow="never" v-loading="loadingHealth">
          <template #header>
            <div class="flex items-center justify-between">
              <span class="font-semibold">{{ t("aegis.settings.systemHealth") }}</span>
              <el-button size="small" @click="loadHealth">{{ t("aegis.common.refresh") }}</el-button>
            </div>
          </template>

          <div v-if="health" class="space-y-4">
            <div class="grid grid-cols-2 gap-3">
              <div
                v-for="comp in health.components"
                :key="comp.name"
                class="border rounded p-3 flex items-center justify-between"
              >
                <div>
                  <div class="font-medium text-sm uppercase">{{ comp.name }}</div>
                  <div class="text-xs text-slate-400">
                    {{ comp.detail || "-" }} · {{ comp.latency_ms }}ms
                  </div>
                </div>
                <el-tag size="small" :type="healthTagType(comp.status)">
                  {{ comp.status }}
                </el-tag>
              </div>
            </div>

            <el-divider content-position="left">{{ t("aegis.settings.clusterSummary") }}</el-divider>
            <div class="grid grid-cols-3 gap-3 text-center">
              <div class="border rounded p-3">
                <div class="text-xs text-slate-400">{{ t("aegis.dashboard.camerasOnline") }}</div>
                <div class="text-xl font-bold mt-1">
                  {{ health.cluster.cameras_online }} / {{ health.cluster.cameras_total }}
                </div>
              </div>
              <div class="border rounded p-3">
                <div class="text-xs text-slate-400">{{ t("aegis.nodes.nodesTab") }}</div>
                <div class="text-xl font-bold mt-1">
                  {{ health.cluster.nodes_online }} / {{ health.cluster.nodes_total }}
                </div>
              </div>
              <div class="border rounded p-3">
                <div class="text-xs text-slate-400">{{ t("aegis.nodes.tasksTab") }}</div>
                <div class="text-xl font-bold mt-1">
                  {{ health.cluster.running_tasks }}
                </div>
              </div>
            </div>
          </div>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>
