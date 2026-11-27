<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  createAlgorithm,
  createAlgorithmVersion,
  deleteAlgorithm,
  listAlgorithms,
  listAlgorithmVersions,
  updateAlgorithmVersion,
  type AlgorithmItem,
  type AlgorithmVersionItem
} from "@/api/aegis";

defineOptions({ name: "AegisAlgorithms" });

const { t } = useI18n();
const loading = ref(false);
const algorithms = ref<AlgorithmItem[]>([]);
const versionsByAlgo = reactive<Record<string, AlgorithmVersionItem[]>>({});

const algoDialog = ref(false);
const algoForm = reactive({
  algorithm_key: "",
  name: "",
  description: "",
  supported_labels: "person,no_helmet"
});

const verDialog = ref(false);
const targetAlgoId = ref("");
const verForm = reactive({
  version: "v1.0.0",
  runtime_key: "py-yolo-v1",
  default_params: '{"target_fps":5}',
  status: "active"
});

async function loadData() {
  loading.value = true;
  try {
    const res = await listAlgorithms();
    algorithms.value = res?.data ?? [];
    for (const a of algorithms.value) {
      const vRes = await listAlgorithmVersions(a.id);
      versionsByAlgo[a.id] = vRes?.data ?? [];
    }
  } finally {
    loading.value = false;
  }
}

async function submitAlgo() {
  await createAlgorithm({
    algorithm_key: algoForm.algorithm_key,
    name: algoForm.name,
    description: algoForm.description,
    supported_labels: algoForm.supported_labels
      .split(",")
      .map(s => s.trim())
      .filter(Boolean)
  });
  algoDialog.value = false;
  ElMessage.success("OK");
  await loadData();
}

async function removeAlgo(id: string) {
  await ElMessageBox.confirm(t("aegis.common.confirmDelete"));
  await deleteAlgorithm(id);
  await loadData();
}

function openAddVersion(algoId: string) {
  targetAlgoId.value = algoId;
  verDialog.value = true;
}

async function submitVersion() {
  let defaultParams = {};
  try {
    defaultParams = JSON.parse(verForm.default_params || "{}");
  } catch {
    ElMessage.error("Invalid JSON in default_params");
    return;
  }
  await createAlgorithmVersion(targetAlgoId.value, {
    version: verForm.version,
    runtime_key: verForm.runtime_key,
    default_params: defaultParams,
    status: verForm.status
  });
  verDialog.value = false;
  ElMessage.success("OK");
  await loadData();
}

async function toggleVersionStatus(v: AlgorithmVersionItem) {
  const nextStatus = v.status === "active" ? "deprecated" : "active";
  await updateAlgorithmVersion(v.id, {
    runtime_key: v.runtime_key,
    launch_spec: v.launch_spec ?? {},
    default_params: v.default_params ?? {},
    status: nextStatus
  });
  await loadData();
}

onMounted(loadData);
</script>

<template>
  <div class="p-4">
    <el-card shadow="never">
      <div class="flex justify-between mb-4">
        <el-button type="primary" @click="algoDialog = true">
          {{ t("aegis.common.create") }}
        </el-button>
        <el-button @click="loadData">{{ t("aegis.common.refresh") }}</el-button>
      </div>

      <el-table v-loading="loading" :data="algorithms" border row-key="id">
        <el-table-column type="expand">
          <template #default="{ row }">
            <div class="p-3">
              <div class="flex justify-between items-center mb-2">
                <span class="font-medium text-sm">{{ t("aegis.algorithms.versions") }}</span>
                <el-button size="small" type="primary" @click="openAddVersion(row.id)">
                  {{ t("aegis.algorithms.addVersion") }}
                </el-button>
              </div>
              <el-table :data="versionsByAlgo[row.id] ?? []" size="small" border>
                <el-table-column prop="version" :label="t('aegis.algorithms.version')" width="110" />
                <el-table-column prop="runtime_key" :label="t('aegis.algorithms.runtimeKey')" width="170" />
                <el-table-column prop="status" :label="t('aegis.common.status')" width="100" />
                <el-table-column :label="t('aegis.algorithms.hits30d')" width="100">
                  <template #default="{ row: ver }">
                    {{ ver.stats_30d?.total_alarms ?? 0 }}
                  </template>
                </el-table-column>
                <el-table-column :label="t('aegis.algorithms.confirmed30d')" width="100">
                  <template #default="{ row: ver }">
                    {{ ver.stats_30d?.confirmed_count ?? 0 }}
                  </template>
                </el-table-column>
                <el-table-column :label="t('aegis.algorithms.fp30d')" width="90">
                  <template #default="{ row: ver }">
                    {{ ver.stats_30d?.false_positive_count ?? 0 }}
                  </template>
                </el-table-column>
                <el-table-column :label="t('aegis.algorithms.fpRate30d')" width="120">
                  <template #default="{ row: ver }">
                    <el-tag
                      size="small"
                      :type="(ver.stats_30d?.false_positive_rate ?? 0) > 25 ? 'danger' : (ver.stats_30d?.false_positive_rate ?? 0) > 10 ? 'warning' : 'success'"
                    >
                      {{ (ver.stats_30d?.false_positive_rate ?? 0).toFixed(1) }}%
                    </el-tag>
                  </template>
                </el-table-column>
                <el-table-column :label="t('aegis.common.actions')" width="120">
                  <template #default="{ row: ver }">
                    <el-button link type="primary" @click="toggleVersionStatus(ver)">
                      {{ ver.status === "active" ? "Deprecate" : "Activate" }}
                    </el-button>
                  </template>
                </el-table-column>
              </el-table>
            </div>
          </template>
        </el-table-column>
        <el-table-column prop="algorithm_key" :label="t('aegis.algorithms.key')" width="180" />
        <el-table-column prop="name" :label="t('aegis.algorithms.name')" min-width="160" />
        <el-table-column prop="description" label="Description" min-width="200" />
        <el-table-column :label="t('aegis.common.actions')" width="160">
          <template #default="{ row }">
            <el-button link type="primary" @click="openAddVersion(row.id)">
              {{ t("aegis.algorithms.addVersion") }}
            </el-button>
            <el-button link type="danger" @click="removeAlgo(row.id)">
              {{ t("aegis.common.delete") }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="algoDialog" :title="t('aegis.common.create')" width="500px">
      <el-form label-width="130px">
        <el-form-item :label="t('aegis.algorithms.key')">
          <el-input v-model="algoForm.algorithm_key" placeholder="person_detect" />
        </el-form-item>
        <el-form-item :label="t('aegis.algorithms.name')">
          <el-input v-model="algoForm.name" />
        </el-form-item>
        <el-form-item :label="t('aegis.algorithms.labels')">
          <el-input v-model="algoForm.supported_labels" />
        </el-form-item>
        <el-form-item label="Description">
          <el-input v-model="algoForm.description" type="textarea" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="algoDialog = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" @click="submitAlgo">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="verDialog" :title="t('aegis.algorithms.addVersion')" width="500px">
      <el-form label-width="140px">
        <el-form-item :label="t('aegis.algorithms.version')">
          <el-input v-model="verForm.version" />
        </el-form-item>
        <el-form-item :label="t('aegis.algorithms.runtimeKey')">
          <el-input v-model="verForm.runtime_key" />
        </el-form-item>
        <el-form-item :label="t('aegis.algorithms.defaultParams')">
          <el-input v-model="verForm.default_params" type="textarea" :rows="3" />
        </el-form-item>
        <el-form-item :label="t('aegis.common.status')">
          <el-select v-model="verForm.status" class="w-full">
            <el-option label="active" value="active" />
            <el-option label="draft" value="draft" />
            <el-option label="deprecated" value="deprecated" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="verDialog = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" @click="submitVersion">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>
