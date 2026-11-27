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

function versionStatusType(status: string): "success" | "warning" | "info" {
  if (status === "active") return "success";
  if (status === "deprecated") return "warning";
  return "info";
}

async function loadData() {
  loading.value = true;
  try {
    const res = await listAlgorithms();
    algorithms.value = res?.data ?? [];
    await Promise.all(
      algorithms.value.map(async a => {
        const vRes = await listAlgorithmVersions(a.id);
        versionsByAlgo[a.id] = vRes?.data ?? [];
      })
    );
  } catch (err: any) {
    ElMessage.error(err?.message || "Failed to load algorithms");
  } finally {
    loading.value = false;
  }
}

async function submitAlgo() {
  try {
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
  } catch (err: any) {
    ElMessage.error(err?.message || "Failed to create algorithm");
  }
}

async function removeAlgo(id: string) {
  try {
    await ElMessageBox.confirm(t("aegis.common.confirmDelete"));
    await deleteAlgorithm(id);
    await loadData();
  } catch (err: any) {
    if (err !== "cancel" && err !== "close") {
      ElMessage.error(err?.message || "Failed to delete algorithm");
    }
  }
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
  try {
    await createAlgorithmVersion(targetAlgoId.value, {
      version: verForm.version,
      runtime_key: verForm.runtime_key,
      default_params: defaultParams,
      status: verForm.status
    });
    verDialog.value = false;
    ElMessage.success("OK");
    await loadData();
  } catch (err: any) {
    ElMessage.error(err?.message || "Failed to create algorithm version");
  }
}

async function toggleVersionStatus(v: AlgorithmVersionItem) {
  try {
    const nextStatus = v.status === "active" ? "deprecated" : "active";
    await updateAlgorithmVersion(v.id, {
      runtime_key: v.runtime_key,
      launch_spec: v.launch_spec ?? {},
      default_params: v.default_params ?? {},
      status: nextStatus
    });
    await loadData();
  } catch (err: any) {
    ElMessage.error(err?.message || "Failed to update version status");
  }
}

onMounted(loadData);
</script>

<template>
  <div class="aegis-page">
    <section class="aegis-panel">
      <header class="aegis-page-header">
        <div class="aegis-page-title-group">
          <h1 class="aegis-page-title">{{ t("menus.aegisAlgorithms") }}</h1>
        </div>
        <div class="aegis-header-actions">
          <el-button type="primary" @click="algoDialog = true">
            {{ t("aegis.common.create") }}
          </el-button>
          <el-button @click="loadData">{{ t("aegis.common.refresh") }}</el-button>
        </div>
      </header>

      <div
        class="aegis-table-wrap"
        tabindex="0"
        role="region"
        :aria-label="t('menus.aegisAlgorithms')"
      >
        <el-table v-loading="loading" :data="algorithms" border stripe row-key="id">
          <el-table-column type="expand">
            <template #default="{ row }">
              <div class="p-3 bg-[var(--el-fill-color-lighter)]">
                <div class="flex justify-between items-center mb-2">
                  <span class="font-medium text-sm">{{ t("aegis.algorithms.versions") }}</span>
                  <el-button size="small" type="primary" @click="openAddVersion(row.id)">
                    {{ t("aegis.algorithms.addVersion") }}
                  </el-button>
                </div>
                <div class="aegis-table-wrap" tabindex="0" role="region" :aria-label="t('aegis.algorithms.versions')">
                  <el-table :data="versionsByAlgo[row.id] ?? []" size="small" border>
                    <el-table-column prop="version" :label="t('aegis.algorithms.version')" width="110" />
                    <el-table-column prop="runtime_key" :label="t('aegis.algorithms.runtimeKey')" min-width="160" show-overflow-tooltip>
                      <template #default="{ row: ver }">
                        <span class="aegis-mono-id">{{ ver.runtime_key }}</span>
                      </template>
                    </el-table-column>
                    <el-table-column :label="t('aegis.common.status')" width="106">
                      <template #default="{ row: ver }">
                        <el-tag size="small" :type="versionStatusType(ver.status)">
                          {{ ver.status }}
                        </el-tag>
                      </template>
                    </el-table-column>
                    <el-table-column :label="t('aegis.algorithms.hits30d')" width="96" align="right">
                      <template #default="{ row: ver }">
                        {{ ver.stats_30d?.total_alarms ?? 0 }}
                      </template>
                    </el-table-column>
                    <el-table-column :label="t('aegis.algorithms.confirmed30d')" width="96" align="right">
                      <template #default="{ row: ver }">
                        {{ ver.stats_30d?.confirmed_count ?? 0 }}
                      </template>
                    </el-table-column>
                    <el-table-column :label="t('aegis.algorithms.fp30d')" width="86" align="right">
                      <template #default="{ row: ver }">
                        {{ ver.stats_30d?.false_positive_count ?? 0 }}
                      </template>
                    </el-table-column>
                    <el-table-column :label="t('aegis.algorithms.fpRate30d')" width="116">
                      <template #default="{ row: ver }">
                        <el-tag
                          size="small"
                          :type="(ver.stats_30d?.false_positive_rate ?? 0) > 25 ? 'danger' : (ver.stats_30d?.false_positive_rate ?? 0) > 10 ? 'warning' : 'success'"
                        >
                          {{ (ver.stats_30d?.false_positive_rate ?? 0).toFixed(1) }}%
                        </el-tag>
                      </template>
                    </el-table-column>
                    <el-table-column :label="t('aegis.common.actions')" width="116" fixed="right">
                      <template #default="{ row: ver }">
                        <el-button link type="primary" @click="toggleVersionStatus(ver)">
                          {{ ver.status === "active" ? "Deprecate" : "Activate" }}
                        </el-button>
                      </template>
                    </el-table-column>
                  </el-table>
                </div>
              </div>
            </template>
          </el-table-column>
          <el-table-column prop="algorithm_key" :label="t('aegis.algorithms.key')" width="175" show-overflow-tooltip>
            <template #default="{ row }">
              <span class="aegis-mono-id">{{ row.algorithm_key }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="name" :label="t('aegis.algorithms.name')" min-width="155" show-overflow-tooltip />
          <el-table-column :label="t('aegis.algorithms.labels')" min-width="200">
            <template #default="{ row }">
              <div class="flex flex-wrap gap-1">
                <el-tag
                  v-for="label in (row.supported_labels ?? []).slice(0, 5)"
                  :key="label"
                  size="small"
                  type="info"
                  effect="plain"
                >
                  {{ label }}
                </el-tag>
                <span v-if="!row.supported_labels?.length" class="text-xs text-zinc-400">—</span>
              </div>
            </template>
          </el-table-column>
          <el-table-column prop="description" label="Description" min-width="180" show-overflow-tooltip />
          <el-table-column :label="t('aegis.common.actions')" width="165" fixed="right">
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
      </div>
    </section>

    <el-dialog v-model="algoDialog" :title="t('aegis.common.create')" width="500px">
      <el-form label-width="130px" size="small">
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
      <el-form label-width="140px" size="small">
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
