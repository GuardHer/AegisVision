<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  createChannel,
  deleteChannel,
  listChannels,
  testChannel,
  updateChannel,
  type AlarmChannelItem
} from "@/api/aegis";

defineOptions({ name: "AegisChannels" });

const { t } = useI18n();
const loading = ref(false);
const channels = ref<AlarmChannelItem[]>([]);
const dialogVisible = ref(false);
const editingId = ref<string | null>(null);

const form = reactive({
  name: "",
  channel_type: "webhook",
  url: "",
  secret: "",
  timeout_ms: 5000,
  include_evidence_urls: true,
  enabled: true
});

async function loadData() {
  loading.value = true;
  try {
    const res = await listChannels();
    channels.value = res?.data ?? [];
  } finally {
    loading.value = false;
  }
}

function openCreate() {
  editingId.value = null;
  Object.assign(form, {
    name: "",
    channel_type: "webhook",
    url: "",
    secret: "",
    timeout_ms: 5000,
    include_evidence_urls: true,
    enabled: true
  });
  dialogVisible.value = true;
}

function openEdit(row: AlarmChannelItem) {
  editingId.value = row.id;
  Object.assign(form, {
    name: row.name,
    channel_type: row.channel_type,
    url: row.config?.url ?? "",
    secret: row.config?.secret ?? "",
    timeout_ms: row.config?.timeout_ms ?? 5000,
    include_evidence_urls: row.config?.include_evidence_urls ?? true,
    enabled: row.enabled
  });
  dialogVisible.value = true;
}

async function submit() {
  const payload = {
    name: form.name,
    channel_type: form.channel_type,
    config: {
      url: form.url,
      method: "POST",
      secret: form.secret,
      timeout_ms: form.timeout_ms,
      include_evidence_urls: form.include_evidence_urls
    },
    enabled: form.enabled
  };
  if (editingId.value) {
    await updateChannel(editingId.value, payload);
  } else {
    await createChannel(payload);
  }
  dialogVisible.value = false;
  ElMessage.success("OK");
  await loadData();
}

async function onTest(id: string) {
  const res = await testChannel(id);
  ElMessage.info(`HTTP ${res?.data?.status_code} (${res?.data?.latency_ms}ms)`);
}

async function onDelete(id: string) {
  await ElMessageBox.confirm(t("aegis.common.confirmDelete"));
  await deleteChannel(id);
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

      <el-table v-loading="loading" :data="channels" border stripe>
        <el-table-column prop="name" :label="t('aegis.channels.name')" min-width="160" />
        <el-table-column prop="channel_type" :label="t('aegis.channels.type')" width="120" />
        <el-table-column :label="t('aegis.channels.url')" min-width="240">
          <template #default="{ row }">{{ row.config?.url }}</template>
        </el-table-column>
        <el-table-column prop="enabled" :label="t('aegis.common.enabled')" width="100" />
        <el-table-column :label="t('aegis.common.actions')" width="220">
          <template #default="{ row }">
            <el-button link type="success" @click="onTest(row.id)">
              {{ t("aegis.channels.test") }}
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
      <el-form label-width="130px">
        <el-form-item :label="t('aegis.channels.name')">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item :label="t('aegis.channels.url')">
          <el-input v-model="form.url" placeholder="http://192.168.x.x:9000/hook" />
        </el-form-item>
        <el-form-item :label="t('aegis.channels.secret')">
          <el-input v-model="form.secret" show-password />
        </el-form-item>
        <el-form-item :label="t('aegis.channels.timeoutMs')">
          <el-input-number v-model="form.timeout_ms" :min="500" :max="30000" />
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
