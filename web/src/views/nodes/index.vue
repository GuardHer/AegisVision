<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import {
  listNodes,
  listTasks,
  rescheduleTask,
  updateNodeStatus,
  type AgentNodeItem,
  type InferenceTaskItem
} from "@/api/aegis";
import { formatDateTime, shortId } from "@/utils/format";
import { showError, showSuccess } from "@/utils/error";
import { formatNodeStatus, formatTaskState } from "@/utils/aegisLabels";

defineOptions({ name: "AegisNodes" });

const { t } = useI18n();
const activeTab = ref("nodes");
const loading = ref(false);
const loadError = ref("");
const nodes = ref<AgentNodeItem[]>([]);
const tasks = ref<InferenceTaskItem[]>([]);
const failedTaskCount = computed(
  () => tasks.value.filter(item => item.actual_state === "failed" || item.actual_state === "error" || Boolean(item.error_message)).length
);
const nodesPage = ref(1);
const nodesPageSize = ref(20);
const tasksPage = ref(1);
const tasksPageSize = ref(20);

const pagedNodes = computed(() => {
  const start = (nodesPage.value - 1) * nodesPageSize.value;
  return nodes.value.slice(start, start + nodesPageSize.value);
});

const pagedTasks = computed(() => {
  const start = (tasksPage.value - 1) * tasksPageSize.value;
  return tasks.value.slice(start, start + tasksPageSize.value);
});

function nodeStatusType(status: string): "success" | "warning" | "danger" | "info" {
  if (status === "online") return "success";
  if (status === "draining") return "warning";
  if (status === "offline") return "danger";
  return "info";
}

function taskStateType(state: string): "success" | "warning" | "danger" | "info" {
  if (state === "running") return "success";
  if (state === "starting" || state === "pending") return "warning";
  if (state === "failed" || state === "error") return "danger";
  return "info";
}

async function loadData() {
  loading.value = true;
  try {
    const [nRes, tRes] = await Promise.all([listNodes(), listTasks()]);
    nodes.value = nRes?.data ?? [];
    tasks.value = tRes?.data ?? [];
    loadError.value = "";
  } catch (err: any) {
    loadError.value = showError(err, t("aegis.common.loadFailed"));
  } finally {
    loading.value = false;
  }
}

async function toggleDrain(node: AgentNodeItem) {
  try {
    const next = node.status === "draining" ? "online" : "draining";
    await updateNodeStatus(node.node_id, next);
    showSuccess(`${node.node_id} → ${formatNodeStatus(next, t)}`);
    await loadData();
  } catch (err: any) {
    showError(err);
  }
}

async function onReschedule(taskId: string) {
  try {
    await rescheduleTask(taskId);
    showSuccess(t("aegis.nodes.rescheduleSuccess"));
    await loadData();
  } catch (err: any) {
    showError(err);
  }
}

function pct(val: any): number {
  const n = Number(val);
  if (!Number.isFinite(n) || n < 0) return 0;
  return Math.min(100, Math.round(n));
}

function barStatus(val: number): "" | "warning" | "exception" {
  if (val >= 90) return "exception";
  if (val >= 75) return "warning";
  return "";
}

onMounted(loadData);
</script>

<template>
  <div class="aegis-page aegis-page--fixed">
    <section class="aegis-panel aegis-panel--fill">
      <header class="aegis-page-header">
        <div class="aegis-page-title-group">
          <h1 class="aegis-page-title">{{ t("menus.aegisNodes") }}</h1>
        </div>
        <div class="aegis-header-actions">
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

      <el-alert
        v-else-if="!loading && nodes.length === 0"
        type="warning"
        show-icon
        :closable="false"
        :title="t('aegis.nodes.noNodesHint')"
        class="mb-3 shrink-0"
      />

      <el-alert
        v-else-if="!loading && failedTaskCount > 0"
        type="error"
        show-icon
        :closable="false"
        :title="`${t('aegis.nodes.failedTasksBanner')} (${failedTaskCount})`"
        class="mb-3 shrink-0"
      />

      <el-tabs v-model="activeTab" class="aegis-tabs--fill">
        <el-tab-pane :label="`${t('aegis.nodes.nodesTab')} (${nodes.length})`" name="nodes">
          <div class="aegis-table-wrap aegis-table-wrap--fill">
            <el-table v-loading="loading" :data="pagedNodes" height="100%" border stripe>
              <el-table-column prop="node_id" :label="t('aegis.nodes.nodeId')" min-width="150" show-overflow-tooltip>
                <template #default="{ row }">
                  <span class="aegis-mono-id">{{ row.node_id }}</span>
                </template>
              </el-table-column>
              <el-table-column prop="hostname" :label="t('aegis.nodes.hostname')" width="145" show-overflow-tooltip />
              <el-table-column prop="ip_addr" :label="t('aegis.nodes.ipAddr')" width="132">
                <template #default="{ row }">
                  <span class="aegis-mono-id">{{ row.ip_addr || "—" }}</span>
                </template>
              </el-table-column>
              <el-table-column :label="t('aegis.nodes.runtimeKeys')" min-width="165">
                <template #default="{ row }">
                  <div class="flex flex-wrap gap-1">
                    <el-tag v-for="rk in row.runtime_keys ?? []" :key="rk" size="small" effect="plain">
                      {{ rk }}
                    </el-tag>
                  </div>
                </template>
              </el-table-column>
              <el-table-column :label="t('aegis.nodes.telemetry')" min-width="250">
                <template #default="{ row }">
                  <div class="space-y-1 text-xs">
                    <div class="flex items-center gap-2">
                      <span class="w-10 text-slate-500">{{ t("aegis.nodes.cpu") }}</span>
                      <el-progress
                        :percentage="pct(row.meta?.cpu_percent)"
                        :status="barStatus(pct(row.meta?.cpu_percent))"
                        :stroke-width="8"
                        class="flex-1"
                      />
                    </div>
                    <div class="flex items-center gap-2">
                      <span class="w-10 text-slate-500">{{ t("aegis.nodes.memory") }}</span>
                      <el-progress
                        :percentage="pct(row.meta?.memory_percent)"
                        :status="barStatus(pct(row.meta?.memory_percent))"
                        :stroke-width="8"
                        class="flex-1"
                      />
                    </div>
                    <template v-if="row.meta?.has_gpu">
                      <div class="flex items-center gap-2">
                        <span class="w-10 text-slate-500">{{ t("aegis.nodes.gpu") }}</span>
                        <el-progress
                          :percentage="pct(row.meta?.gpu_percent)"
                          :status="barStatus(pct(row.meta?.gpu_percent))"
                          :stroke-width="8"
                          class="flex-1"
                        />
                        <span v-if="row.meta?.gpu_temp_c" class="text-slate-500 aegis-tabular">
                          {{ Math.round(row.meta.gpu_temp_c) }}°C
                        </span>
                      </div>
                      <div class="flex items-center gap-2">
                        <span class="w-10 text-slate-500">{{ t("aegis.nodes.vram") }}</span>
                        <el-progress
                          :percentage="pct(row.meta?.gpu_memory_percent)"
                          :status="barStatus(pct(row.meta?.gpu_memory_percent))"
                          :stroke-width="8"
                          class="flex-1"
                        />
                      </div>
                    </template>
                    <div v-else class="text-slate-400">
                      {{ t("aegis.nodes.noGpuMetrics") }}
                    </div>
                  </div>
                </template>
              </el-table-column>
              <el-table-column :label="t('aegis.nodes.load')" width="88" align="center">
                <template #default="{ row }">
                  <span class="aegis-tabular font-medium">{{ row.running_tasks }} / {{ row.max_tasks }}</span>
                </template>
              </el-table-column>
              <el-table-column :label="t('aegis.common.status')" width="102">
                <template #default="{ row }">
                  <el-tag size="small" :type="nodeStatusType(row.status)">
                    {{ formatNodeStatus(row.status, t) }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column :label="t('aegis.nodes.lastHeartbeat')" width="166">
                <template #default="{ row }">
                  <span class="aegis-tabular">{{ formatDateTime(row.last_heartbeat_at) }}</span>
                </template>
              </el-table-column>
              <el-table-column :label="t('aegis.common.actions')" width="118" fixed="right">
                <template #default="{ row }">
                  <el-popconfirm
                    :title="row.status === 'draining' ? t('aegis.nodes.confirmResume') : t('aegis.nodes.confirmDrain')"
                    width="240"
                    @confirm="toggleDrain(row)"
                  >
                    <template #reference>
                      <el-button link type="warning">
                        {{ row.status === "draining" ? t("aegis.nodes.resume") : t("aegis.nodes.drain") }}
                      </el-button>
                    </template>
                  </el-popconfirm>
                </template>
              </el-table-column>
            </el-table>
          </div>
          <div class="aegis-pagination">
            <el-pagination
              v-model:current-page="nodesPage"
              v-model:page-size="nodesPageSize"
              :page-sizes="[10, 20, 50, 100]"
              :total="nodes.length"
              layout="total, sizes, prev, pager, next"
              @size-change="nodesPage = 1"
            />
          </div>
        </el-tab-pane>

        <el-tab-pane :label="`${t('aegis.nodes.tasksTab')} (${tasks.length})`" name="tasks">
          <div class="aegis-table-wrap aegis-table-wrap--fill">
            <el-table v-loading="loading" :data="pagedTasks" height="100%" border stripe>
              <el-table-column :label="t('aegis.nodes.taskId')" width="140">
                <template #default="{ row }">
                  <el-tooltip :content="row.id" placement="top">
                    <span class="aegis-mono-id">{{ shortId(row.id, 12) }}</span>
                  </el-tooltip>
                </template>
              </el-table-column>
              <el-table-column prop="runtime_key" :label="t('aegis.nodes.runtimeKey')" min-width="140" show-overflow-tooltip>
                <template #default="{ row }">
                  <span class="aegis-mono-id">{{ row.runtime_key }}</span>
                </template>
              </el-table-column>
              <el-table-column prop="assigned_node_id" :label="t('aegis.nodes.assignedNode')" min-width="140" show-overflow-tooltip>
                <template #default="{ row }">
                  <span class="aegis-mono-id">{{ row.assigned_node_id || "—" }}</span>
                </template>
              </el-table-column>
              <el-table-column :label="t('aegis.nodes.desiredState')" width="102">
                <template #default="{ row }">
                  <el-tag size="small" type="info" effect="plain">
                    {{ formatTaskState(row.desired_state, t) }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column :label="t('aegis.nodes.actualState')" width="108">
                <template #default="{ row }">
                  <el-tag size="small" :type="taskStateType(row.actual_state)">
                    {{ formatTaskState(row.actual_state, t) }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column :label="t('aegis.nodes.leaseExpires')" width="166">
                <template #default="{ row }">
                  <span class="aegis-tabular">{{ formatDateTime(row.lease_expires_at) }}</span>
                </template>
              </el-table-column>
              <el-table-column :label="t('aegis.nodes.lastFrame')" width="166">
                <template #default="{ row }">
                  <span class="aegis-tabular">{{ formatDateTime(row.last_frame_at) }}</span>
                </template>
              </el-table-column>
              <el-table-column :label="t('aegis.nodes.errorMessage')" min-width="180" show-overflow-tooltip>
                <template #default="{ row }">
                  <el-tag v-if="row.error_message" size="small" type="danger" effect="plain">
                    {{ row.error_message }}
                  </el-tag>
                  <span v-else class="text-slate-400">—</span>
                </template>
              </el-table-column>
              <el-table-column :label="t('aegis.common.actions')" width="118" fixed="right">
                <template #default="{ row }">
                  <el-popconfirm
                    :title="t('aegis.nodes.confirmReschedule')"
                    width="240"
                    @confirm="onReschedule(row.id)"
                  >
                    <template #reference>
                      <el-button link type="primary">
                        {{ t("aegis.nodes.reschedule") }}
                      </el-button>
                    </template>
                  </el-popconfirm>
                </template>
              </el-table-column>
            </el-table>
          </div>
          <div class="aegis-pagination">
            <el-pagination
              v-model:current-page="tasksPage"
              v-model:page-size="tasksPageSize"
              :page-sizes="[10, 20, 50, 100]"
              :total="tasks.length"
              layout="total, sizes, prev, pager, next"
              @size-change="tasksPage = 1"
            />
          </div>
        </el-tab-pane>
      </el-tabs>
    </section>
  </div>
</template>
