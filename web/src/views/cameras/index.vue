<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox, ElNotification } from "element-plus";
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
import { isCancelError, showError, showSuccess } from "@/utils/error";
import { formatCameraStatus, formatZoneType } from "@/utils/aegisLabels";
import { useRenderIcon } from "@/components/ReIcon/src/hooks";
import Plus from "~icons/ep/plus";
import EditPen from "~icons/ep/edit-pen";
import Delete from "~icons/ep/delete";

defineOptions({ name: "AegisCameras" });

const { t } = useI18n();
const router = useRouter();

const loading = ref(false);
const loadError = ref("");
const savingZone = ref(false);
const savingCamera = ref(false);
const savingMove = ref(false);
const importingCsv = ref(false);
const zonesTree = ref<ZoneTreeNode[]>([]);
const selectedZoneId = ref<string>("");
const cameras = ref<CameraItem[]>([]);
const selectedCameraIds = ref<string[]>([]);
const filterKeyword = ref("");
const filterStatus = ref("");
const page = ref(1);
const pageSize = ref(20);

const IconPlus = useRenderIcon(Plus);
const IconEditPen = useRenderIcon(EditPen);
const IconDelete = useRenderIcon(Delete);

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

const pagedCameras = computed(() => {
  const start = (page.value - 1) * pageSize.value;
  return filteredCameras.value.slice(start, start + pageSize.value);
});

watch([filterKeyword, filterStatus, selectedZoneId], () => {
  page.value = 1;
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
  try {
    const res = await getZonesTree();
    zonesTree.value = res?.data ?? [];
  } catch (err: any) {
    loadError.value = showError(err, t("aegis.common.loadFailed"));
  }
}

async function loadCameras() {
  loading.value = true;
  try {
    const res = await listCameras(selectedZoneId.value || undefined);
    cameras.value = res?.data ?? [];
    loadError.value = "";
  } catch (err: any) {
    loadError.value = showError(err, t("aegis.common.loadFailed"));
  } finally {
    loading.value = false;
  }
}

async function loadAll() {
  loadError.value = "";
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
  if (!zoneForm.name.trim()) {
    ElMessage.warning(t("aegis.cameras.zoneNameRequired"));
    return;
  }
  savingZone.value = true;
  try {
    if (editingZoneId.value) {
      await updateZone(editingZoneId.value, { ...zoneForm });
    } else {
      await createZone({ ...zoneForm });
    }
    showSuccess(t("aegis.common.saveSuccess"));
    zoneDialogVisible.value = false;
    await loadZones();
  } catch (err: any) {
    showError(err);
  } finally {
    savingZone.value = false;
  }
}

async function onDeleteZone(node: ZoneTreeNode) {
  try {
    await ElMessageBox.confirm(
      t("aegis.common.confirmDelete"),
      t("aegis.common.confirmTitle"),
      { type: "warning" }
    );
    await deleteZone(node.id);
    if (selectedZoneId.value === node.id) selectedZoneId.value = "";
    showSuccess(t("aegis.common.deleteSuccess"));
    await loadAll();
  } catch (err: any) {
    if (isCancelError(err)) return;
    showError(err, t("aegis.errors.zoneNotEmpty"));
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
  if (!form.name.trim() || !form.rtsp_url.trim()) {
    ElMessage.warning(t("aegis.cameras.nameAndRtspRequired"));
    return;
  }
  const payload = {
    name: form.name.trim(),
    zone_id: form.zone_id,
    workstation_code: form.workstation_code.trim(),
    tags: form.tags_text
      .split(",")
      .map(s => s.trim())
      .filter(Boolean),
    location: form.location.trim(),
    rtsp_url: form.rtsp_url.trim(),
    sub_stream_url: form.sub_stream_url.trim(),
    codec_hint: form.codec_hint,
    enabled: form.enabled
  };
  savingCamera.value = true;
  try {
    if (editingId.value) {
      await updateCamera(editingId.value, payload);
    } else {
      await createCamera(payload);
    }
    showSuccess(t("aegis.common.saveSuccess"));
    dialogVisible.value = false;
    await loadAll();
  } catch (err: any) {
    showError(err);
  } finally {
    savingCamera.value = false;
  }
}

async function onDeleteCamera(id: string) {
  try {
    await ElMessageBox.confirm(
      t("aegis.common.confirmDelete"),
      t("aegis.common.confirmTitle"),
      { type: "warning" }
    );
    await deleteCamera(id);
    showSuccess(t("aegis.common.deleteSuccess"));
    await loadAll();
  } catch (err: any) {
    if (isCancelError(err)) return;
    showError(err);
  }
}

function onSelectionChange(rows: CameraItem[]) {
  selectedCameraIds.value = rows.map(r => r.id);
}

async function runBatchEnable(enabled: boolean) {
  if (selectedCameraIds.value.length === 0) return;
  try {
    const res = await batchUpdateCameras({
      camera_ids: selectedCameraIds.value,
      action: enabled ? "enable" : "disable"
    });
    ElNotification({
      title: t("aegis.common.actionSuccess"),
      message: `${res?.data?.updated_count ?? selectedCameraIds.value.length}`,
      type: "success",
      duration: 3000
    });
    await loadAll();
  } catch (err: any) {
    showError(err);
  }
}

function openMoveZone() {
  if (selectedCameraIds.value.length === 0) return;
  moveTargetZoneId.value = selectedZoneId.value;
  moveDialogVisible.value = true;
}

async function submitMoveZone() {
  savingMove.value = true;
  try {
    await batchUpdateCameras({
      camera_ids: selectedCameraIds.value,
      action: "move_zone",
      zone_id: moveTargetZoneId.value
    });
    moveDialogVisible.value = false;
    showSuccess(t("aegis.common.saveSuccess"));
    await loadAll();
  } catch (err: any) {
    showError(err);
  } finally {
    savingMove.value = false;
  }
}

async function submitImportCSV() {
  importingCsv.value = true;
  try {
    const res = await importCamerasCSV(importCSVText.value);
    importResult.value = {
      created_count: res?.data?.created_count ?? 0,
      errors: res?.data?.errors ?? []
    };
    ElNotification({
      title: t("aegis.cameras.importPartialSummary"),
      message: `${t("aegis.common.createdCount")}: ${importResult.value.created_count}${importResult.value.errors.length ? ` · ${t("aegis.common.errorCount")}: ${importResult.value.errors.length}` : ""}`,
      type: importResult.value.errors.length ? "warning" : "success",
      duration: 4500
    });
    await loadAll();
  } catch (err: any) {
    showError(err);
  } finally {
    importingCsv.value = false;
  }
}

onMounted(loadAll);
</script>

<template>
  <div class="aegis-page aegis-page--fixed">
    <div class="aegis-split-layout">
      <!-- Left Spatial Zone Tree -->
      <aside class="aegis-panel aegis-panel--fill gap-3">
        <div class="flex items-center justify-between gap-2 pb-2 shrink-0 border-b border-[var(--el-border-color-extra-light)]">
          <span class="font-semibold text-sm">{{ t("aegis.cameras.zone") }}</span>
          <el-button size="small" type="primary" link @click="openCreateZone('')">
            + {{ t("aegis.cameras.addZone") }}
          </el-button>
        </div>

        <el-button
          size="small"
          :type="selectedZoneId === '' ? 'primary' : 'default'"
          plain
          class="w-full shrink-0"
          @click="clearZoneFilter"
        >
          {{ t("aegis.cameras.allZones") }}
        </el-button>

        <el-scrollbar class="flex-1 min-h-0">
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
                  <el-tooltip :content="t('aegis.cameras.addChildZone')" placement="top">
                    <el-button
                      link
                      type="primary"
                      size="small"
                      :icon="IconPlus"
                      @click.stop="openCreateZone(data.id)"
                    />
                  </el-tooltip>
                  <el-tooltip :content="t('aegis.common.edit')" placement="top">
                    <el-button
                      link
                      type="primary"
                      size="small"
                      :icon="IconEditPen"
                      @click.stop="openEditZone(data)"
                    />
                  </el-tooltip>
                  <el-tooltip :content="t('aegis.common.delete')" placement="top">
                    <el-button
                      link
                      type="danger"
                      size="small"
                      :icon="IconDelete"
                      @click.stop="onDeleteZone(data)"
                    />
                  </el-tooltip>
                </span>
              </div>
            </template>
          </el-tree>
        </el-scrollbar>
      </aside>

      <!-- Right Camera Assets Panel -->
      <section class="aegis-panel aegis-panel--fill">
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
            <el-option :label="formatCameraStatus('online', t)" value="online" />
            <el-option :label="formatCameraStatus('offline', t)" value="offline" />
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
              <el-button size="small" type="danger" plain @click="loadAll">
                {{ t("aegis.common.retry") }}
              </el-button>
            </div>
          </template>
        </el-alert>

        <div class="aegis-table-wrap aegis-table-wrap--fill">
          <el-table
            v-loading="loading"
            :data="pagedCameras"
            height="100%"
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
                  {{ formatCameraStatus(row.status, t) }}
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

        <div class="aegis-pagination">
          <el-pagination
            v-model:current-page="page"
            v-model:page-size="pageSize"
            :page-sizes="[10, 20, 50, 100]"
            :total="filteredCameras.length"
            layout="total, sizes, prev, pager, next"
            @size-change="page = 1"
          />
        </div>
      </section>
    </div>

    <!-- Zone CRUD Dialog -->
    <el-dialog v-model="zoneDialogVisible" :title="editingZoneId ? t('aegis.common.edit') : t('aegis.cameras.addZone')" width="460px">
      <el-form label-width="110px" size="small">
        <el-form-item :label="t('aegis.cameras.zone')">
          <el-select v-model="zoneForm.parent_id" clearable class="w-full" :placeholder="t('aegis.cameras.rootZone')">
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
            <el-option :label="formatZoneType('factory', t)" value="factory" />
            <el-option :label="formatZoneType('workshop', t)" value="workshop" />
            <el-option :label="formatZoneType('line', t)" value="line" />
            <el-option :label="formatZoneType('workstation', t)" value="workstation" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="zoneDialogVisible = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" :loading="savingZone" @click="submitZone">{{ t("aegis.common.save") }}</el-button>
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
          <el-input v-model="form.tags_text" :placeholder="t('aegis.cameras.tagsPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.location')">
          <el-input v-model="form.location" />
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.rtspUrl')">
          <el-input v-model="form.rtsp_url" placeholder="rtsp://..." />
        </el-form-item>
        <el-form-item :label="t('aegis.cameras.subStreamUrl')">
          <el-input v-model="form.sub_stream_url" :placeholder="t('aegis.cameras.subStreamPlaceholder')" />
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
        <el-button type="primary" :loading="savingCamera" @click="submitCamera">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>

    <!-- Batch Move Zone Dialog -->
    <el-dialog v-model="moveDialogVisible" :title="t('aegis.cameras.batchMoveZone')" width="420px">
      <el-select v-model="moveTargetZoneId" clearable class="w-full" :placeholder="t('aegis.common.unassigned')">
        <el-option v-for="z in flatZones" :key="z.id" :label="z.label" :value="z.id" />
      </el-select>
      <template #footer>
        <el-button @click="moveDialogVisible = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" :loading="savingMove" @click="submitMoveZone">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>

    <!-- CSV Import Dialog -->
    <el-dialog v-model="importDialogVisible" :title="t('aegis.cameras.importCsv')" width="600px">
      <el-alert
        type="info"
        show-icon
        :closable="false"
        :title="t('aegis.cameras.importHint')"
        class="mb-3"
      />
      <el-input v-model="importCSVText" type="textarea" :rows="8" />
      <el-alert
        v-if="importResult"
        :type="importResult.errors.length ? 'warning' : 'success'"
        show-icon
        :closable="false"
        :title="`${t('aegis.common.createdCount')}: ${importResult.created_count}${importResult.errors.length ? ` · ${t('aegis.common.errorCount')}: ${importResult.errors.length}` : ''}`"
        class="mt-3"
      >
        <template v-if="importResult.errors.length" #default>
          <ul class="m-0 pl-4 text-xs space-y-0.5">
            <li v-for="(e, idx) in importResult.errors" :key="idx">{{ e }}</li>
          </ul>
        </template>
      </el-alert>
      <template #footer>
        <el-button @click="importDialogVisible = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" :loading="importingCsv" @click="submitImportCSV">{{ t("aegis.common.save") }}</el-button>
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

.zone-tree-row:hover .zone-tree-row__actions {
  opacity: 1;
}

.zone-tree-row__actions .el-button + .el-button {
  margin-left: 2px;
}
</style>
