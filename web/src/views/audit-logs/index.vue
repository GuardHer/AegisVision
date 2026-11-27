<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import { listAuditLogs, type AuditLogItem } from "@/api/aegis";
import { formatDateTime, shortId } from "@/utils/format";

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
  void loadData();
}

function resetFilters() {
  query.actor_username = "";
  query.resource_type = "";
  query.action = "";
  query.dateRange = [];
  query.page = 1;
  void loadData();
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
  <div class="aegis-page">
    <section class="aegis-panel">
      <header class="aegis-page-header">
        <div class="aegis-page-title-group">
          <h1 class="aegis-page-title">{{ t("aegis.auditLogs.title") }}</h1>
        </div>
        <div class="aegis-header-actions">
          <el-button @click="loadData">{{ t("aegis.common.refresh") }}</el-button>
        </div>
      </header>

      <div class="aegis-filter-bar">
        <el-input
          v-model="query.actor_username"
          :placeholder="t('aegis.auditLogs.actor')"
          clearable
          style="width: 155px"
          @keyup.enter="handleSearch"
        />
        <el-select
          v-model="query.resource_type"
          :placeholder="t('aegis.auditLogs.resourceType')"
          clearable
          style="width: 165px"
          @change="handleSearch"
        >
          <el-option v-for="rt in resourceOptions" :key="rt" :label="rt" :value="rt" />
        </el-select>
        <el-input
          v-model="query.action"
          :placeholder="t('aegis.auditLogs.action')"
          clearable
          style="width: 165px"
          @keyup.enter="handleSearch"
        />
        <el-date-picker
          v-model="query.dateRange"
          type="datetimerange"
          range-separator="-"
          style="width: 310px"
          @change="handleSearch"
        />
        <div class="aegis-filter-bar__end">
          <el-button @click="resetFilters">{{ t("aegis.common.reset") }}</el-button>
          <el-button type="primary" @click="handleSearch">{{ t("aegis.common.refresh") }}</el-button>
        </div>
      </div>

      <div class="aegis-table-wrap">
        <el-table v-loading="loading" :data="items" border stripe row-key="id">
          <el-table-column type="expand">
            <template #default="{ row }">
              <div class="p-3 bg-[var(--el-fill-color-lighter)] rounded">
                <div class="text-xs font-medium mb-1 text-slate-500">
                  {{ t("aegis.auditLogs.detail") }} (Request ID: {{ row.request_id || "—" }})
                </div>
                <pre class="text-xs overflow-auto m-0 aegis-mono-id">{{ prettyJSON(row.detail) }}</pre>
              </div>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.auditLogs.createdAt')" width="170">
            <template #default="{ row }">
              <span class="aegis-tabular">{{ formatDateTime(row.created_at) }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="actor_username" :label="t('aegis.auditLogs.actor')" width="128" show-overflow-tooltip />
          <el-table-column :label="t('aegis.auditLogs.action')" width="175">
            <template #default="{ row }">
              <el-tag size="small" type="info" effect="plain">{{ row.action }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="resource_type" :label="t('aegis.auditLogs.resourceType')" width="135" />
          <el-table-column :label="t('aegis.auditLogs.resourceId')" width="125">
            <template #default="{ row }">
              <span class="aegis-mono-id" :title="row.resource_id">{{ shortId(row.resource_id, 10) }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="summary" :label="t('aegis.auditLogs.summary')" min-width="220" show-overflow-tooltip />
          <el-table-column :label="t('aegis.auditLogs.clientIp')" width="130">
            <template #default="{ row }">
              <span class="aegis-mono-id">{{ row.client_ip || "—" }}</span>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <div class="aegis-pagination">
        <el-pagination
          v-model:current-page="query.page"
          v-model:page-size="query.page_size"
          :page-sizes="[20, 50, 100]"
          :total="total"
          layout="total, sizes, prev, pager, next"
          @current-change="loadData"
          @size-change="handleSearch"
        />
      </div>
    </section>
  </div>
</template>
