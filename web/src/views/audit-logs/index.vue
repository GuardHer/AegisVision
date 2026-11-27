<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import { listAuditLogs, type AuditLogItem } from "@/api/aegis";

defineOptions({ name: "AegisAuditLogs" });

const { t } = useI18n();
const loading = ref(false);
const items = ref<AuditLogItem[]>([]);
const total = ref(0);

const query = reactive({
  actor_username: "",
  resource_type: "",
  action: "",
  dateRange: [] as string[],
  page: 1,
  page_size: 20
});

const resourceOptions = [
  "user",
  "zone",
  "camera",
  "rule",
  "schedule",
  "algorithm",
  "node",
  "alarm",
  "alarm_channel",
  "system_settings"
];

async function loadData() {
  loading.value = true;
  try {
    const params: Record<string, any> = {
      page: query.page,
      page_size: query.page_size
    };
    if (query.actor_username.trim()) params.actor_username = query.actor_username.trim();
    if (query.resource_type) params.resource_type = query.resource_type;
    if (query.action.trim()) params.action = query.action.trim();
    if (query.dateRange?.length === 2) {
      params.start_time = new Date(query.dateRange[0]).toISOString();
      params.end_time = new Date(query.dateRange[1]).toISOString();
    }
    const res = await listAuditLogs(params);
    items.value = res?.data?.items ?? [];
    total.value = res?.data?.total ?? 0;
  } catch (err: any) {
    ElMessage.error(err?.message || "Failed to load audit logs");
  } finally {
    loading.value = false;
  }
}

function handleSearch() {
  query.page = 1;
  loadData();
}

function prettyJSON(obj: any): string {
  try {
    return JSON.stringify(obj ?? {}, null, 2);
  } catch {
    return "{}";
  }
}

onMounted(loadData);
</script>

<template>
  <div class="p-4">
    <el-card shadow="never">
      <div class="flex flex-wrap items-center justify-between gap-3 mb-4">
        <div class="text-base font-semibold">{{ t("aegis.auditLogs.title") }}</div>
        <div class="flex flex-wrap items-center gap-2">
          <el-input
            v-model="query.actor_username"
            :placeholder="t('aegis.auditLogs.actor')"
            clearable
            style="width: 150px"
            @keyup.enter="handleSearch"
          />
          <el-select
            v-model="query.resource_type"
            :placeholder="t('aegis.auditLogs.resourceType')"
            clearable
            style="width: 160px"
            @change="handleSearch"
          >
            <el-option v-for="rt in resourceOptions" :key="rt" :label="rt" :value="rt" />
          </el-select>
          <el-input
            v-model="query.action"
            :placeholder="t('aegis.auditLogs.action')"
            clearable
            style="width: 160px"
            @keyup.enter="handleSearch"
          />
          <el-date-picker
            v-model="query.dateRange"
            type="datetimerange"
            range-separator="-"
            style="width: 310px"
            @change="handleSearch"
          />
          <el-button type="primary" @click="handleSearch">{{ t("aegis.common.refresh") }}</el-button>
        </div>
      </div>

      <el-table v-loading="loading" :data="items" border stripe row-key="id">
        <el-table-column type="expand">
          <template #default="{ row }">
            <div class="p-3 bg-slate-50 dark:bg-slate-900 rounded">
              <div class="text-xs font-medium mb-1 text-slate-500">
                {{ t("aegis.auditLogs.detail") }} (Request ID: {{ row.request_id || "-" }})
              </div>
              <pre class="text-xs overflow-auto m-0">{{ prettyJSON(row.detail) }}</pre>
            </div>
          </template>
        </el-table-column>
        <el-table-column :label="t('aegis.auditLogs.createdAt')" width="185">
          <template #default="{ row }">
            {{ row.created_at?.slice(0, 19).replace("T", " ") }}
          </template>
        </el-table-column>
        <el-table-column prop="actor_username" :label="t('aegis.auditLogs.actor')" width="130" />
        <el-table-column :label="t('aegis.auditLogs.action')" width="170">
          <template #default="{ row }">
            <el-tag size="small" type="info">{{ row.action }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="resource_type" :label="t('aegis.auditLogs.resourceType')" width="140" />
        <el-table-column prop="summary" :label="t('aegis.auditLogs.summary')" min-width="220" show-overflow-tooltip />
        <el-table-column prop="client_ip" :label="t('aegis.auditLogs.clientIp')" width="130" />
      </el-table>

      <div class="flex justify-end mt-4">
        <el-pagination
          v-model:current-page="query.page"
          :page-size="query.page_size"
          :total="total"
          layout="total, prev, pager, next"
          @current-change="loadData"
        />
      </div>
    </el-card>
  </div>
</template>
