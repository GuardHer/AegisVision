<script setup lang="ts">
import { onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import {
  listNodes,
  listTasks,
  rescheduleTask,
  updateNodeStatus,
  type AgentNodeItem,
  type InferenceTaskItem
} from "@/api/aegis";
import { formatDateTime, shortId } from "@/utils/format";

defineOptions({ name: "AegisNodes" });

const { t } = useI18n();
const activeTab = ref("nodes");
const loading = ref(false);
const nodes = ref<AgentNodeItem[]>([]);
const tasks = ref<InferenceTaskItem[]>([]);

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
  } catch (err: any) {
    ElMessage.error(err?.message || "Failed to load compute cluster");
  } finally {
    loading.value = false;
  }
}

async function toggleDrain(node: AgentNodeItem) {
  try {
    const next = node.status === "draining" ? "online" : "draining";
    await updateNodeStatus(node.node_id, next);
    ElMessage.success(`Node ${node.node_id} -> ${next}`);
    await loadData();
  } catch (err: any) {
    ElMessage.error(err?.message || "Failed to update node status");
  }
}

async function onReschedule(taskId: string) {
  try {
    await rescheduleTask(taskId);
    ElMessage.success("Reschedule requested");
    await loadData();
  } catch (err: any) {
    ElMessage.error(err?.message || "Failed to reschedule task");
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
  <div class="aegis-page">
    <section class="aegis-panel">
      <header class="aegis-page-header">
        <div class="aegis-page-title-group">
          <h1 class="aegis-page-title">{{ t("menus.aegisNodes") }}</h1>
        </div>
        <div class="aegis-header-actions">
          <el-button @click="loadData">{{ t("aegis.common.refresh") }}</el-button>
        </div>
      </header>

      <el-tabs v-model="activeTab">
        <el-tab-pane :label="`${t('aegis.nodes.nodesTab')} (${nodes.length})`" name="nodes">
          <div class="aegis-table-wrap" tabindex="0" role="region" :aria-label="t('aegis.nodes.nodesTab')">
            <el-table v-loading="loading" :data="nodes" border stripe>
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
                    {{ row.status }}
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
                  <el-button link type="warning" @click="toggleDrain(row)">
                    {{ row.status === "draining" ? t("aegis.nodes.resume") : t("aegis.nodes.drain") }}
                  </el-button>
                </template>
              </el-table-column>
            </el-table>
          </div>
        </el-tab-pane>

        <el-tab-pane :label="`${t('aegis.nodes.tasksTab')} (${tasks.length})`" name="tasks">
          <div class="aegis-table-wrap" tabindex="0" role="region" :aria-label="t('aegis.nodes.tasksTab')">
            <el-table v-loading="loading" :data="tasks" border stripe>
              <el-table-column label="Task ID" width="140">
                <template #default="{ row }">
                  <span class="aegis-mono-id" :title="row.id">{{ shortId(row.id, 12) }}</span>
                </template>
              </el-table-column>
              <el-table-column prop="runtime_key" label="Runtime Key" min-width="140" show-overflow-tooltip>
                <template #default="{ row }">
                  <span class="aegis-mono-id">{{ row.runtime_key }}</span>
                </template>
              </el-table-column>
              <el-table-column prop="assigned_node_id" label="Node" min-width="140" show-overflow-tooltip>
                <template #default="{ row }">
                  <span class="aegis-mono-id">{{ row.assigned_node_id || "—" }}</span>
                </template>
              </el-table-column>
              <el-table-column label="Desired" width="102">
                <template #default="{ row }">
                  <el-tag size="small" type="info" effect="plain">{{ row.desired_state }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column label="Actual" width="108">
                <template #default="{ row }">
                  <el-tag size="small" :type="taskStateType(row.actual_state)">{{ row.actual_state }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column label="Lease Expires" width="166">
                <template #default="{ row }">
                  <span class="aegis-tabular">{{ formatDateTime(row.lease_expires_at) }}</span>
                </template>
              </el-table-column>
              <el-table-column label="Last Frame" width="166">
                <template #default="{ row }">
                  <span class="aegis-tabular">{{ formatDateTime(row.last_frame_at) }}</span>
                </template>
              </el-table-column>
              <el-table-column :label="t('aegis.common.actions')" width="118" fixed="right">
                <template #default="{ row }">
                  <el-button link type="primary" @click="onReschedule(row.id)">
                    {{ t("aegis.nodes.reschedule") }}
                  </el-button>
                </template>
              </el-table-column>
            </el-table>
          </div>
        </el-tab-pane>
      </el-tabs>
    </section>
  </div>
</template>
