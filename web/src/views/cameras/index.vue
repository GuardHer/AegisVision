<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  createCamera,
  deleteCamera,
  listCameras,
  updateCamera,
  type CameraItem
} from "@/api/aegis";

defineOptions({ name: "AegisCameras" });

const { t } = useI18n();
const router = useRouter();

const loading = ref(false);
const cameras = ref<CameraItem[]>([]);
const dialogVisible = ref(false);
const editingId = ref<string | null>(null);
const form = reactive({
  name: "",
  location: "",
  rtsp_url: "",
  sub_stream_url: "",
  codec_hint: "h264",
  enabled: true
});

async function loadData() {
  loading.value = true;
  try {
    const res = await listCameras();
    cameras.value = res?.data ?? [];
  } finally {
    loading.value = false;
  }
}

function openCreate() {
  editingId.value = null;
  Object.assign(form, {
    name: "",
    location: "",
    rtsp_url: "",
    sub_stream_url: "",
    codec_hint: "h264",
    enabled: true
  });
  dialogVisible.value = true;
}

function openEdit(row: CameraItem) {
  editingId.value = row.id;
  Object.assign(form, {
    name: row.name,
    location: row.location,
    rtsp_url: row.rtsp_url,
    sub_stream_url: row.sub_stream_url,
    codec_hint: row.codec_hint,
    enabled: row.enabled
  });
  dialogVisible.value = true;
}

async function submit() {
  if (editingId.value) {
    await updateCamera(editingId.value, { ...form });
  } else {
    await createCamera({ ...form });
  }
  ElMessage.success("OK");
  dialogVisible.value = false;
  await loadData();
}

async function onDelete(id: string) {
  await ElMessageBox.confirm(t("aegis.common.confirmDelete"));
  await deleteCamera(id);
  ElMessage.success("OK");
  await loadData();
}

onMounted(loadData);
</script>

<template>
  <div class="p-4">
    <el-card shadow="never">
      <div class="flex justify-between mb-4">
        <el-button type="primary" @click="openCreate">{{ t("aegis.common.create") }}</el-button>
        <el-button @click="loadData">{{ t("aegis.common.refresh") }}</el-button>
      </div>

      <el-table v-loading="loading" :data="cameras" border stripe>
        <el-table-column prop="name" :label="t('aegis.cameras.name')" min-width="150" />
        <el-table-column prop="location" :label="t('aegis.cameras.location')" min-width="140" />
        <el-table-column prop="stream_path" :label="t('aegis.cameras.streamPath')" width="180" />
        <el-table-column prop="codec_hint" :label="t('aegis.cameras.codecHint')" width="90" />
        <el-table-column prop="status" :label="t('aegis.common.status')" width="100" />
        <el-table-column :label="t('aegis.common.actions')" width="260" fixed="right">
          <template #default="{ row }">
            <el-button
              link
              type="primary"
              @click="router.push(`/cameras/${row.id}/config`)"
            >
              {{ t("aegis.cameras.configRoiRules") }}
            </el-button>
            <el-button link type="primary" @click="openEdit(row)">
              {{ t("aegis.common.edit") }}
            </el-button>
            <el-button link type="danger" @click="onDelete(row.id)">
              {{ t("aegis.common.delete") }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? t('aegis.common.edit') : t('aegis.common.create')" width="520px">
      <el-form label-width="120px">
        <el-form-item :label="t('aegis.cameras.name')">
          <el-input v-model="form.name" />
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
        <el-button type="primary" @click="submit">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>
