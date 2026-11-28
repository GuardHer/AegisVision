<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  createSchedule,
  deleteSchedule,
  listSchedules,
  updateSchedule,
  type ArmingScheduleItem,
  type IncludeDateWindow,
  type WeeklyWindow
} from "@/api/aegis";
import { isCancelError, showError, showSuccess } from "@/utils/error";
import { formatWeekday } from "@/utils/aegisLabels";

defineOptions({ name: "AegisSchedules" });

const { t } = useI18n();
const loading = ref(false);
const loadError = ref("");
const saving = ref(false);
const schedules = ref<ArmingScheduleItem[]>([]);
const page = ref(1);
const pageSize = ref(20);
const pagedSchedules = computed(() => {
  const start = (page.value - 1) * pageSize.value;
  return schedules.value.slice(start, start + pageSize.value);
});

const dialogVisible = ref(false);
const editingId = ref<string | null>(null);
const form = reactive({
  name: "",
  description: "",
  timezone: "Asia/Shanghai",
  enabled: true,
  weekly_windows: [] as WeeklyWindow[],
  exclude_dates: [] as string[],
  include_dates: [] as IncludeDateWindow[]
});
const newExcludeDate = ref("");
const newIncludeDate = reactive({ date: "", start: "", end: "" });

const weekdayOptions = computed(() => [1, 2, 3, 4, 5, 6, 7].map(d => ({
  value: d,
  label: formatWeekday(d, t)
})));

function formatWindowDays(days: number[] | undefined): string {
  return (days ?? []).map(d => formatWeekday(d, t)).join("、");
}

async function loadData() {
  loading.value = true;
  try {
    const res = await listSchedules();
    schedules.value = res?.data ?? [];
    loadError.value = "";
  } catch (err: any) {
    loadError.value = showError(err, t("aegis.common.loadFailed"));
  } finally {
    loading.value = false;
  }
}

function openCreate() {
  editingId.value = null;
  form.name = "";
  form.description = "";
  form.timezone = "Asia/Shanghai";
  form.enabled = true;
  form.weekly_windows = [{ days: [1, 2, 3, 4, 5], start: "08:00", end: "20:00" }];
  form.exclude_dates = [];
  form.include_dates = [];
  dialogVisible.value = true;
}

function openEdit(row: ArmingScheduleItem) {
  editingId.value = row.id;
  form.name = row.name;
  form.description = row.description;
  form.timezone = row.timezone || "Asia/Shanghai";
  form.enabled = row.enabled;
  form.weekly_windows = (row.weekly_windows ?? []).map(w => ({
    days: [...(w.days ?? [])],
    start: w.start,
    end: w.end
  }));
  form.exclude_dates = [...(row.exclude_dates ?? [])];
  form.include_dates = (row.include_dates ?? []).map(i => ({ ...i }));
  dialogVisible.value = true;
}

function addWeeklyWindow() {
  form.weekly_windows.push({ days: [1, 2, 3, 4, 5], start: "20:00", end: "08:00" });
}

function removeWeeklyWindow(idx: number) {
  form.weekly_windows.splice(idx, 1);
}

const DATE_RE = /^\d{4}-\d{2}-\d{2}$/;

function addExcludeDate() {
  const d = newExcludeDate.value.trim();
  if (!d) return;
  if (!DATE_RE.test(d)) {
    ElMessage.warning(t("aegis.common.invalidDateFormat"));
    return;
  }
  if (!form.exclude_dates.includes(d)) form.exclude_dates.push(d);
  newExcludeDate.value = "";
}

function addIncludeDate() {
  const d = newIncludeDate.date.trim();
  if (!d) return;
  if (!DATE_RE.test(d)) {
    ElMessage.warning(t("aegis.common.invalidDateFormat"));
    return;
  }
  form.include_dates.push({
    date: d,
    start: newIncludeDate.start.trim() || undefined,
    end: newIncludeDate.end.trim() || undefined
  });
  newIncludeDate.date = "";
  newIncludeDate.start = "";
  newIncludeDate.end = "";
}

async function submitForm() {
  if (!form.name.trim()) {
    ElMessage.warning(t("aegis.schedules.name"));
    return;
  }
  const payload = {
    name: form.name.trim(),
    description: form.description.trim(),
    timezone: form.timezone.trim() || "Asia/Shanghai",
    enabled: form.enabled,
    weekly_windows: form.weekly_windows,
    exclude_dates: form.exclude_dates,
    include_dates: form.include_dates
  };
  saving.value = true;
  try {
    if (editingId.value) {
      await updateSchedule(editingId.value, payload);
    } else {
      await createSchedule(payload);
    }
    dialogVisible.value = false;
    showSuccess(t("aegis.common.saveSuccess"));
    await loadData();
  } catch (err: any) {
    showError(err);
  } finally {
    saving.value = false;
  }
}

async function onDelete(row: ArmingScheduleItem) {
  try {
    await ElMessageBox.confirm(
      t("aegis.common.confirmDelete"),
      t("aegis.common.confirmTitle"),
      { type: "warning" }
    );
    await deleteSchedule(row.id);
    showSuccess(t("aegis.common.deleteSuccess"));
    await loadData();
  } catch (err: any) {
    if (isCancelError(err)) return;
    showError(err, t("aegis.errors.scheduleReferenced"));
  }
}

onMounted(loadData);
</script>

<template>
  <div class="aegis-page aegis-page--fixed">
    <section class="aegis-panel aegis-panel--fill">
      <header class="aegis-page-header">
        <div class="aegis-page-title-group">
          <h1 class="aegis-page-title">{{ t("aegis.schedules.title") }}</h1>
        </div>
        <div class="aegis-header-actions">
          <el-button type="primary" data-test="create-schedule-btn" @click="openCreate">
            {{ t("aegis.common.create") }}
          </el-button>
          <el-button @click="loadData">{{ t("aegis.common.refresh") }}</el-button>
        </div>
      </header>

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
        <el-table v-loading="loading" :data="pagedSchedules" height="100%" border stripe>
          <el-table-column prop="name" :label="t('aegis.schedules.name')" min-width="165" show-overflow-tooltip />
          <el-table-column prop="timezone" :label="t('aegis.schedules.timezone')" width="145" />
          <el-table-column :label="t('aegis.schedules.weeklyWindows')" min-width="240">
            <template #default="{ row }">
              <div class="flex flex-wrap gap-1">
                <el-tag
                  v-for="(w, idx) in row.weekly_windows || []"
                  :key="idx"
                  size="small"
                  :type="w.end < w.start ? 'warning' : 'info'"
                  effect="plain"
                >
                  {{ formatWindowDays(w.days) }} {{ w.start }}-{{ w.end }}
                </el-tag>
              </div>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.schedules.ruleCount')" width="108" align="center">
            <template #default="{ row }">
              <el-tag size="small" type="primary">{{ row.rule_count ?? 0 }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.common.status')" width="96">
            <template #default="{ row }">
              <el-tag :type="row.enabled ? 'success' : 'info'" size="small">
                {{ row.enabled ? t("aegis.common.enabled") : t("aegis.common.disabled") }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.common.actions')" width="145" fixed="right">
            <template #default="{ row }">
              <el-button link type="primary" @click="openEdit(row)">{{ t("aegis.common.edit") }}</el-button>
              <el-button link type="danger" @click="onDelete(row)">{{ t("aegis.common.delete") }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <div class="aegis-pagination">
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="pageSize"
          :page-sizes="[10, 20, 50, 100]"
          :total="schedules.length"
          layout="total, sizes, prev, pager, next"
          @size-change="page = 1"
        />
      </div>
    </section>

    <el-dialog
      v-model="dialogVisible"
      :title="editingId ? t('aegis.common.edit') : t('aegis.common.create')"
      width="640px"
    >
      <el-form label-width="140px" size="small">
        <el-form-item :label="t('aegis.schedules.name')">
          <el-input v-model="form.name" :placeholder="t('aegis.schedules.namePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('aegis.schedules.timezone')">
          <el-input v-model="form.timezone" placeholder="Asia/Shanghai" />
        </el-form-item>
        <el-form-item :label="t('aegis.common.enabled')">
          <el-switch v-model="form.enabled" />
        </el-form-item>

        <el-form-item :label="t('aegis.schedules.weeklyWindows')">
          <div class="w-full flex flex-col gap-2">
            <el-alert
              type="info"
              show-icon
              :closable="false"
              :title="t('aegis.schedules.overnightHint')"
            />
            <div
              v-for="(w, idx) in form.weekly_windows"
              :key="idx"
              class="p-2 rounded border flex flex-col gap-2"
            >
              <div class="flex items-center justify-between gap-2">
                <el-checkbox-group v-model="w.days" size="small">
                  <el-checkbox-button v-for="d in weekdayOptions" :key="d.value" :value="d.value">
                    {{ d.label }}
                  </el-checkbox-button>
                </el-checkbox-group>
                <el-button link type="danger" size="small" @click="removeWeeklyWindow(idx)">
                  {{ t("aegis.common.delete") }}
                </el-button>
              </div>
              <div class="flex items-center gap-2">
                <el-input v-model="w.start" class="w-28" placeholder="08:00" />
                <span>→</span>
                <el-input v-model="w.end" class="w-28" placeholder="20:00" />
                <el-tag v-if="w.end < w.start" size="small" type="warning">{{ t("aegis.schedules.overnight") }}</el-tag>
              </div>
            </div>
            <el-button size="small" @click="addWeeklyWindow">+ {{ t("aegis.schedules.addWindow") }}</el-button>
          </div>
        </el-form-item>

        <el-form-item :label="t('aegis.schedules.excludeDates')">
          <div class="w-full flex flex-col gap-2">
            <div class="flex gap-2">
              <el-input v-model="newExcludeDate" placeholder="2026-10-01" class="w-40" />
              <el-button size="small" @click="addExcludeDate">{{ t("aegis.schedules.addDate") }}</el-button>
            </div>
            <div class="flex flex-wrap gap-1">
              <el-tag
                v-for="(d, idx) in form.exclude_dates"
                :key="d"
                closable
                type="danger"
                @close="form.exclude_dates.splice(idx, 1)"
              >
                {{ d }}
              </el-tag>
            </div>
          </div>
        </el-form-item>

        <el-form-item :label="t('aegis.schedules.includeDates')">
          <div class="w-full flex flex-col gap-2">
            <div class="flex gap-2">
              <el-input v-model="newIncludeDate.date" placeholder="2026-10-03" class="w-32" />
              <el-input v-model="newIncludeDate.start" placeholder="08:00 (opt)" class="w-28" />
              <el-input v-model="newIncludeDate.end" placeholder="20:00 (opt)" class="w-28" />
              <el-button size="small" @click="addIncludeDate">{{ t("aegis.schedules.addDate") }}</el-button>
            </div>
            <div class="flex flex-wrap gap-1">
              <el-tag
                v-for="(inc, idx) in form.include_dates"
                :key="`${inc.date}-${idx}`"
                closable
                type="success"
                @close="form.include_dates.splice(idx, 1)"
              >
                {{ inc.date }} {{ inc.start && inc.end ? `${inc.start}-${inc.end}` : t("aegis.schedules.allDay") }}
              </el-tag>
            </div>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" :loading="saving" @click="submitForm">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>
