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

defineOptions({ name: "AegisNodes" });

const { t } = useI18n();
const activeTab = ref("nodes");
const loading = ref(false);
const nodes = ref<AgentNodeItem[]>([]);
const tasks = ref<InferenceTaskItem[]>([]);

async function loadData() {
  loading.value = true;
  try {
    const [nRes, tRes] = await Promise.all([listNodes(), listTasks()]);
    nodes.value = nRes?.data ?? [];
    tasks.value = tRes?.data ?? [];
  } finally {
    loading.value = false;
  }
}

async function toggleDrain(node: AgentNodeItem) {
  const next = node.status === "draining" ? "online" : "draining";
  await updateNodeStatus(node.node_id, next);
  ElMessage.success(`Node ${node.node_id} -> ${next}`);
  await loadData();
}

async function onReschedule(taskId: string) {
  await rescheduleTask(taskId);
  ElMessage.success("Reschedule requested");
  await loadData();
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
  <div class="p-4">
    <el-card shadow="never">
      <div class="flex justify-end mb-2">
        <el-button @click="loadData">{{ t("aegis.common.refresh") }}</el-button>
      </div>
      <el-tabs v-model="activeTab">
        <el-tab-pane :label="t('aegis.nodes.nodesTab')" name="nodes">
          <el-table v-loading="loading" :data="nodes" border stripe>
            <el-table-column prop="node_id" :label="t('aegis.nodes.nodeId')" min-width="160" />
            <el-table-column prop="hostname" :label="t('aegis.nodes.hostname')" width="150" />
            <el-table-column prop="ip_addr" :label="t('aegis.nodes.ipAddr')" width="140" />
            <el-table-column :label="t('aegis.nodes.runtimeKeys')" min-width="180">
              <template #default="{ row }">
                <el-tag v-for="rk in row.runtime_keys ?? []" :key="rk" size="small" class="mr-1">
                  {{ rk }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column :label="t('aegis.nodes.telemetry')" min-width="260">
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
                      <span v-if="row.meta?.gpu_temp_c" class="text-slate-500">
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
            <el-table-column :label="t('aegis.nodes.load')" width="90">
              <template #default="{ row }">
                {{ row.running_tasks }} / {{ row.max_tasks }}
              </template>
            </el-table-column>
            <el-table-column prop="status" :label="t('aegis.common.status')" width="110" />
            <el-table-column prop="last_heartbeat_at" :label="t('aegis.nodes.lastHeartbeat')" width="190" />
            <el-table-column :label="t('aegis.common.actions')" width="130">
              <template #default="{ row }">
                <el-button link type="warning" @click="toggleDrain(row)">
                  {{ row.status === "draining" ? t("aegis.nodes.resume") : t("aegis.nodes.drain") }}
                </el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-tab-pane>

        <el-tab-pane :label="t('aegis.nodes.tasksTab')" name="tasks">
          <el-table v-loading="loading" :data="tasks" border stripe>
            <el-table-column prop="id" label="Task ID" min-width="180" />
            <el-table-column prop="runtime_key" label="Runtime Key" width="140" />
            <el-table-column prop="assigned_node_id" label="Node" width="140" />
            <el-table-column prop="desired_state" label="Desired" width="100" />
            <el-table-column prop="actual_state" label="Actual" width="110" />
            <el-table-column prop="lease_expires_at" label="Lease Expires" width="190" />
            <el-table-column prop="last_frame_at" label="Last Frame" width="190" />
            <el-table-column :label="t('aegis.common.actions')" width="120">
              <template #default="{ row }">
                <el-button link type="primary" @click="onReschedule(row.id)">
                  {{ t("aegis.nodes.reschedule") }}
                </el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-tab-pane>
      </el-tabs>
    </el-card>
  </div>
</template>
