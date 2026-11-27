<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  batchUpdateCameras,
  createCamera,
  createZone,
  deleteCamera,
  deleteZone,
  getZonesTree,
  importCamerasCSV,
  listCameras,
  updateCamera,
  updateZone,
  type CameraItem,
  type ZoneTreeNode
} from "@/api/aegis";
import { formatDateTime } from "@/utils/format";

defineOptions({ name: "AegisCameras" });

const { t } = useI18n();
const router = useRouter();

const loading = ref(false);
const zonesTree = ref<ZoneTreeNode[]>([]);
const selectedZoneId = ref<string>("");
const cameras = ref<CameraItem[]>([]);
const selectedCameraIds = ref<string[]>([]);
const filterKeyword = ref("");
const filterStatus = ref("");

// Flatten zone tree for dropdowns and table lookup
const flatZones = computed(() => {
  const out: Array<{ id: string; name: string; label: string; zone_type: string }> = [];
  const walk = (nodes: ZoneTreeNode[], depth: number) => {
    for (const n of nodes) {
      out.push({
        id: n.id,
        name: n.name,
        label: `${"　".repeat(depth)}${n.name}`,
        zone_type: n.zone_type
      });
      if (n.children?.length) walk(n.children, depth + 1);
    }
  };
  walk(zonesTree.value, 0);
  return out;
});

const zoneNameMap = computed(() => {
  const m = new Map<string, string>();
  for (const z of flatZones.value) m.set(z.id, z.name);
  return m;
});

const filteredCameras = computed(() => {
  const kw = filterKeyword.value.trim().toLowerCase();
  return cameras.value.filter(cam => {
    if (filterStatus.value && cam.status !== filterStatus.value) return false;
    if (!kw) return true;
    const hay = `${cam.name} ${cam.workstation_code || ""} ${(cam.tags || []).join(" ")}`.toLowerCase();
    return hay.includes(kw);
  });
});

// Zone CRUD modal
const zoneDialogVisible = ref(false);
const editingZoneId = ref<string | null>(null);
const zoneForm = reactive({
  parent_id: "",
  name: "",
  code: "",
  zone_type: "workshop" as "factory" | "workshop" | "line" | "workstation",
  sort_order: 0,
  description: ""
});

// Camera CRUD modal
const dialogVisible = ref(false);
const editingId = ref<string | null>(null);
const form = reactive({
  name: "",
  zone_id: "",
  workstation_code: "",
  tags_text: "",
  location: "",
  rtsp_url: "",
  sub_stream_url: "",
  codec_hint: "h264",
  enabled: true
});

// Batch move modal
const moveDialogVisible = ref(false);
const moveTargetZoneId = ref("");

// CSV import modal
const importDialogVisible = ref(false);
const importCSVText = ref(
  "name,rtsp_url,sub_stream_url,workstation_code,zone_id,codec_hint\n"
);
const importResult = ref<{ created_count: number; errors: string[] } | null>(null);

async function loadZones() {
  const res = await getZonesTree();
  zonesTree.value = res?.data ?? [];
}

async function loadCameras() {
  loading.value = true;
  try {
    const res = await listCameras(selectedZoneId.value || undefined);
    cameras.value = res?.data ?? [];
  } finally {
    loading.value = false;
  }
}

async function loadAll() {
  await Promise.all([loadZones(), loadCameras()]);
}

function selectZone(node: ZoneTreeNode) {
  selectedZoneId.value = node.id;
  void loadCameras();
}

function clearZoneFilter() {
  selectedZoneId.value = "";
  void loadCameras();
}

function openCreateZone(parentId = "") {
  editingZoneId.value = null;
  Object.assign(zoneForm, {
    parent_id: parentId,
    name: "",
    code: "",
    zone_type: parentId ? "workshop" : "factory",
    sort_order: 0,
    description: ""
  });
  zoneDialogVisible.value = true;
}

function openEditZone(node: ZoneTreeNode) {
  editingZoneId.value = node.id;
  Object.assign(zoneForm, {
    parent_id: node.parent_id || "",
    name: node.name,
    code: node.code,
    zone_type: node.zone_type,
    sort_order: node.sort_order,
    description: node.description
  });
  zoneDialogVisible.value = true;
}

async function submitZone() {
  if (!zoneForm.name.trim()) return;
  try {
    if (editingZoneId.value) {
      await updateZone(editingZoneId.value, { ...zoneForm });
    } else {
      await createZone({ ...zoneForm });
    }
    ElMessage.success("OK");
    zoneDialogVisible.value = false;
    await loadZones();
  } catch (err: any) {
    ElMessage.error(err?.response?.data?.message || err?.message || "Failed to save zone");
  }
}

async function onDeleteZone(node: ZoneTreeNode) {
  try {
    await ElMessageBox.confirm(t("aegis.common.confirmDelete"));
    await deleteZone(node.id);
    if (selectedZoneId.value === node.id) selectedZoneId.value = "";
    ElMessage.success("OK");
    await loadAll();
  } catch (err: any) {
    if (err === "cancel" || err === "close") return;
    ElMessage.error(err?.response?.data?.message || "Cannot delete non-empty zone");
  }
}

function openCreateCamera() {
  editingId.value = null;
  Object.assign(form, {
    name: "",
    zone_id: selectedZoneId.value,
    workstation_code: "",
    tags_text: "",
    location: "",
    rtsp_url: "",
    sub_stream_url: "",
    codec_hint: "h264",
    enabled: true
  });
  dialogVisible.value = true;
}

function openEditCamera(row: CameraItem) {
  editingId.value = row.id;
  Object.assign(form, {
    name: row.name,
    zone_id: row.zone_id || "",
    workstation_code: row.workstation_code || "",
    tags_text: (row.tags ?? []).join(", "),
    location: row.location,
    rtsp_url: row.rtsp_url,
    sub_stream_url: row.sub_stream_url,
    codec_hint: row.codec_hint,
    enabled: row.enabled
  });
  dialogVisible.value = true;
}

async function submitCamera() {
  const payload = {
    name: form.name,
    zone_id: form.zone_id,
    workstation_code: form.workstation_code,
    tags: form.tags_text
      .split(",")
      .map(s => s.trim())
      .filter(Boolean),
    location: form.location,
    rtsp_url: form.rtsp_url,
    sub_stream_url: form.sub_stream_url,
    codec_hint: form.codec_hint,
    enabled: form.enabled
  };
  try {
    if (editingId.value) {
      await updateCamera(editingId.value, payload);
    } else {
      await createCamera(payload);
    }
    ElMessage.success("OK");
    dialogVisible.value = false;
    await loadAll();
  } catch (err: any) {
    ElMessage.error(err?.response?.data?.message || err?.message || "Failed to save camera");
  }
}

async function onDeleteCamera(id: string) {
  try {
    await ElMessageBox.confirm(t("aegis.common.confirmDelete"));
    await deleteCamera(id);
    ElMessage.success("OK");
    await loadAll();
  } catch (err: any) {
    if (err === "cancel" || err === "close") return;
    ElMessage.error(err?.response?.data?.message || err?.message || "Failed to delete camera");
  }
}

function onSelectionChange(rows: CameraItem[]) {
  selectedCameraIds.value = rows.map(r => r.id);
}

async function runBatchEnable(enabled: boolean) {
  if (selectedCameraIds.value.length === 0) return;
  try {
    await batchUpdateCameras({
      camera_ids: selectedCameraIds.value,
      action: enabled ? "enable" : "disable"
    });
    ElMessage.success("OK");
    await loadAll();
  } catch (err: any) {
    ElMessage.error(err?.response?.data?.message || err?.message || "Batch update failed");
  }
}

function openMoveZone() {
  if (selectedCameraIds.value.length === 0) return;
  moveTargetZoneId.value = selectedZoneId.value;
  moveDialogVisible.value = true;
}

async function submitMoveZone() {
  try {
    await batchUpdateCameras({
      camera_ids: selectedCameraIds.value,
      action: "move_zone",
      zone_id: moveTargetZoneId.value
    });
    moveDialogVisible.value = false;
    ElMessage.success("OK");
    await loadAll();
  } catch (err: any) {
    ElMessage.error(err?.response?.data?.message || err?.message || "Batch move failed");
  }
}

async function submitImportCSV() {
  try {
    const res = await importCamerasCSV(importCSVText.value);
    importResult.value = {
      created_count: res?.data?.created_count ?? 0,
      errors: res?.data?.errors ?? []
    };
    ElMessage.success(`Imported ${importResult.value.created_count} cameras`);
    await loadAll();
  } catch (err: any) {
    ElMessage.error(err?.response?.data?.message || err?.message || "CSV import failed");
  }
}

onMounted(loadAll);
</script>

<template>
  <div class="aegis-page">
    <div class="aegis-split-layout">
      <!-- Left Spatial Zone Tree -->
      <aside class="aegis-panel flex flex-col gap-3">
        <div class="flex items-center justify-between gap-2 pb-2 border-b border-[var(--el-border-color-extra-light)]">
          <span class="font-semibold text-sm">{{ t("aegis.cameras.zone") }}</span>
          <el-button size="small" type="primary" link @click="openCreateZone('')">
            + {{ t("aegis.cameras.addZone") }}
          </el-button>
        </div>

        <el-button
          size="small"
          :type="selectedZoneId === '' ? 'primary' : 'default'"
          plain
          class="w-full"
          @click="clearZoneFilter"
        >
          {{ t("aegis.cameras.allZones") }}
        </el-button>

        <div class="aegis-scroll-region max-h-[calc(100dvh-260px)]">
          <el-tree
            :data="zonesTree"
            node-key="id"
            default-expand-all
            highlight-current
            :expand-on-click-node="false"
            @node-click="selectZone"
          >
            <template #default="{ data }">
              <div class="zone-tree-row">
                <span class="zone-tree-row__label" :title="data.name">
                  {{ data.name }}
                  <span class="text-zinc-400 aegis-tabular">({{ data.online_camera_count }}/{{ data.camera_count }})</span>
                </span>
                <span class="zone-tree-row__actions">
                  <button
                    type="button"
                    class="zone-action-btn"
                    :title="t('aegis.cameras.addChildZone')"
                    :aria-label="t('aegis.cameras.addChildZone')"
                    @click.stop="openCreateZone(data.id)"
                  >
                    +
                  </button>
                  <button
                    type="button"
                    class="zone-action-btn"
                    :title="t('aegis.common.edit')"
                    :aria-label="t('aegis.common.edit')"
                    @click.stop="openEditZone(data)"
                  >
                    {{ t("aegis.common.edit") }}
                  </button>
                  <button
                    type="button"
                    class="zone-action-btn zone-action-btn--danger"
                    :title="t('aegis.common.delete')"
                    :aria-label="t('aegis.common.delete')"
                    @click.stop="onDeleteZone(data)"
                  >
                    {{ t("aegis.common.delete") }}
                  </button>
                </span>
              </div>
            </template>
          </el-tree>
        </div>
      </aside>

      <!-- Right Camera Assets Panel -->
      <section class="aegis-panel">
        <header class="aegis-page-header">
          <div class="aegis-page-title-group">
            <div class="flex items-center gap-2">
              <h1 class="aegis-page-title">{{ t("menus.aegisCameras") }}</h1>
              <el-tag v-if="selectedZoneId" size="small" closable @close="clearZoneFilter">
                {{ zoneNameMap.get(selectedZoneId) || selectedZoneId }}
              </el-tag>
            </div>
          </div>
          <div class="aegis-header-actions">
            <el-button type="primary" @click="openCreateCamera">{{ t("aegis.common.create") }}</el-button>
            <el-button @click="importResult = null; importDialogVisible = true">
              {{ t("aegis.cameras.importCsv") }}
            </el-button>
            <el-button @click="loadAll">{{ t("aegis.common.refresh") }}</el-button>
          </div>
        </header>

        <div class="aegis-filter-bar">
          <el-input
            v-model="filterKeyword"
            clearable
            :placeholder="t('aegis.monitor.searchCamera')"
            style="width: 210px"
          />
          <el-select
            v-model="filterStatus"
            clearable
            :placeholder="t('aegis.common.status')"
            style="width: 125px"
          >
            <el-option label="online" value="online" />
            <el-option label="offline" value="offline" />
          </el-select>

          <div class="aegis-filter-bar__end">
            <el-button size="small" :disabled="selectedCameraIds.length === 0" @click="runBatchEnable(true)">
              {{ t("aegis.cameras.batchEnable") }}
            </el-button>
            <el-button size="small" :disabled="selectedCameraIds.length === 0" @click="runBatchEnable(false)">
              {{ t("aegis.cameras.batchDisable") }}
            </el-button>
            <el-button size="small" :disabled="selectedCameraIds.length === 0" @click="openMoveZone">
              {{ t("aegis.cameras.batchMoveZone") }}
            </el-button>
          </div>
        </div>

        <div
          class="aegis-table-wrap"
          tabindex="0"
          role="region"
          :aria-label="t('menus.aegisCameras')"
        >
          <el-table
            v-loading="loading"
            :data="filteredCameras"
            border
            stripe
            @selection-change="onSelectionChange"
          >
            <el-table-column type="selection" width="46" />
            <el-table-column :label="t('aegis.cameras.name')" min-width="160" show-overflow-tooltip>
              <template #default="{ row }">
                <div class="font-medium truncate">{{ row.name }}</div>
                <div v-if="row.tags?.length" class="flex flex-wrap gap-1 mt-0.5">
                  <el-tag v-for="tag in row.tags.slice(0, 3)" :key="tag" size="small" type="info" effect="plain">
                    {{ tag }}
                  </el-tag>
                </div>
              </template>
            </el-table-column>
            <el-table-column :label="t('aegis.cameras.workstationCode')" width="118" show-overflow-tooltip>
              <template #default="{ row }">
                <span class="aegis-mono-id">{{ row.workstation_code || "—" }}</span>
              </template>
            </el-table-column>
            <el-table-column :label="t('aegis.cameras.zone')" min-width="125" show-overflow-tooltip>
              <template #default="{ row }">
                {{ row.zone_id ? zoneNameMap.get(row.zone_id) || "—" : "—" }}
              </template>
            </el-table-column>
            <el-table-column :label="t('aegis.cameras.codecHint')" width="86">
              <template #default="{ row }">
                <el-tag size="small" type="info" effect="plain">
                  {{ (row.codec_hint || "h264").toUpperCase() }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column :label="t('aegis.common.status')" width="92">
              <template #default="{ row }">
                <el-tag :type="row.status === 'online' ? 'success' : 'info'" size="small">
                  {{ row.status }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column :label="t('aegis.common.enabled')" width="86">
              <template #default="{ row }">
                <el-tag :type="row.enabled ? 'success' : 'danger'" size="small" effect="plain">
                  {{ row.enabled ? t("aegis.common.enabled") : t("aegis.common.disabled") }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column :label="t('aegis.cameras.lastOnline')" width="164">
              <template #default="{ row }">
                <span class="aegis-tabular">{{ formatDateTime(row.last_online_at) }}</span>
              </template>
            </el-table-column>
            <el-table-column :label="t('aegis.common.actions')" width="235" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" @click="router.push(`/cameras/${row.id}/config`)">
                  {{ t("aegis.cameras.configRoiRules") }}
                </el-button>
                <el-button link type="primary" @click="openEditCamera(row)">
                  {{ t("aegis.common.edit") }}
                </el-button>
                <el-button link type="danger" @click="onDeleteCamera(row.id)">
                  {{ t("aegis.common.delete") }}
                </el-button>
              </template>
            </el-table-column>
          </el-table>
        </div>
      </section>
    </div>

    <!-- Zone CRUD Dialog -->
    <el-dialog v-model="zoneDialogVisible" :title="editingZoneId ? t('aegis.common.edit') : t('aegis.cameras.addZone')" width="460px">
      <el-form label-width="110px" size="small">
        <el-form-item :label="t('aegis.cameras.zone')">
          <el-select v-model="zoneForm.parent_id" clearable class="w-full" placeholder="Root (Factory)">
            <el-option v-for="z in flatZones" :key="z.id" :label="z.label" :value="z.id" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.name')">
          <el-input v-model="zoneForm.name" />
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.zoneCode')">
          <el-input v-model="zoneForm.code" />
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.zoneType')">
          <el-select v-model="zoneForm.zone_type" class="w-full">
            <el-option label="factory" value="factory" />
            <el-option label="workshop" value="workshop" />
            <el-option label="line" value="line" />
            <el-option label="workstation" value="workstation" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="zoneDialogVisible = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" @click="submitZone">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>

    <!-- Camera CRUD Dialog -->
    <el-dialog v-model="dialogVisible" :title="editingId ? t('aegis.common.edit') : t('aegis.common.create')" width="540px">
      <el-form label-width="120px" size="small">
        <el-form-item :label="t('aegis.cameras.name')">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.zone')">
          <el-select v-model="form.zone_id" clearable class="w-full">
            <el-option v-for="z in flatZones" :key="z.id" :label="z.label" :value="z.id" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.workstationCode')">
          <el-input v-model="form.workstation_code" placeholder="WS-01" />
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.tags')">
          <el-input v-model="form.tags_text" placeholder="safety, sop" />
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.location')">
          <el-input v-model="form.location" />
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.rtspUrl')">
          <el-input v-model="form.rtsp_url" placeholder="rtsp://..." />
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.subStreamUrl')">
          <el-input v-model="form.sub_stream_url" placeholder="rtsp://... (optional)" />
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.codecHint')">
          <el-select v-model="form.codec_hint" class="w-full">
            <el-option label="H.264" value="h264" />
            <el-option label="H.265" value="h265" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('aegis.common.enabled')">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" @click="submitCamera">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>

    <!-- Batch Move Zone Dialog -->
    <el-dialog v-model="moveDialogVisible" :title="t('aegis.cameras.batchMoveZone')" width="420px">
      <el-select v-model="moveTargetZoneId" clearable class="w-full" placeholder="Unassigned">
        <el-option v-for="z in flatZones" :key="z.id" :label="z.label" :value="z.id" />
      </el-select>
      <template #footer>
        <el-button @click="moveDialogVisible = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" @click="submitMoveZone">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>

    <!-- CSV Import Dialog -->
    <el-dialog v-model="importDialogVisible" :title="t('aegis.cameras.importCsv')" width="600px">
      <div class="text-xs text-zinc-500 mb-2">{{ t("aegis.cameras.importHint") }}</div>
      <el-input v-model="importCSVText" type="textarea" :rows="8" />
      <div v-if="importResult" class="mt-2 text-xs">
        <div class="text-emerald-600">Created: {{ importResult.created_count }}</div>
        <div v-for="(e, idx) in importResult.errors" :key="idx" class="text-red-500">{{ e }}</div>
      </div>
      <template #footer>
        <el-button @click="importDialogVisible = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" @click="submitImportCSV">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
.zone-tree-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  inline-size: 100%;
  min-inline-size: 0;
  padding-inline-end: 4px;
  font-size: 12px;
}

.zone-tree-row__label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-inline-size: 0;
}

.zone-tree-row__actions {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
  opacity: 0;
  transition: opacity 150ms ease;
}

.zone-tree-row:hover .zone-tree-row__actions,
.zone-tree-row:focus-within .zone-tree-row__actions {
  opacity: 1;
}

.zone-action-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-block-size: 24px;
  padding: 0 4px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--el-color-primary);
  font-size: 11px;
  cursor: pointer;

  &:hover {
    background: var(--el-color-primary-light-9);
  }
}

.zone-action-btn--danger {
  color: var(--el-color-danger);

  &:hover {
    background: var(--el-color-danger-light-9);
  }
}
</style>
