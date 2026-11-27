<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  createChannel,
  deleteChannel,
  getZonesTree,
  listChannels,
  resetChannel,
  testChannel,
  updateChannel,
  type AlarmChannelItem,
  type ZoneTreeNode
} from "@/api/aegis";

defineOptions({ name: "AegisChannels" });

const { t } = useI18n();
const loading = ref(false);
const channels = ref<AlarmChannelItem[]>([]);
const zones = ref<ZoneTreeNode[]>([]);
const dialogVisible = ref(false);
const editingId = ref<string | null>(null);

const form = reactive({
  name: "",
  channel_type: "webhook" as "webhook" | "wecom" | "dingtalk" | "sound_light",
  url: "",
  webhook_url: "",
  trigger_url: "",
  reset_url: "",
  auto_reset_seconds: 60,
  secret: "",
  timeout_ms: 5000,
  include_evidence_urls: true,
  enabled: true,
  auto_route: false,
  severities: [] as string[],
  zone_ids: [] as string[],
  rule_modes: [] as string[]
});

function typeLabel(ct: string): string {
  switch (ct) {
    case "wecom":
      return t("aegis.channels.typeWecom");
    case "dingtalk":
      return t("aegis.channels.typeDingtalk");
    case "sound_light":
      return t("aegis.channels.typeSoundLight");
    default:
      return t("aegis.channels.typeWebhook");
  }
}

function targetUrlOf(row: AlarmChannelItem): string {
  if (row.channel_type === "sound_light") {
    return row.config?.trigger_url ?? "";
  }
  if (row.channel_type === "wecom" || row.channel_type === "dingtalk") {
    return row.config?.webhook_url ?? row.config?.url ?? "";
  }
  return row.config?.url ?? "";
}

async function loadData() {
  loading.value = true;
  try {
    const [chRes, zRes] = await Promise.all([listChannels(), getZonesTree().catch(() => ({ data: [] }))]);
    channels.value = chRes?.data ?? [];
    zones.value = zRes?.data ?? [];
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
    webhook_url: "",
    trigger_url: "",
    reset_url: "",
    auto_reset_seconds: 60,
    secret: "",
    timeout_ms: 5000,
    include_evidence_urls: true,
    enabled: true,
    auto_route: false,
    severities: [],
    zone_ids: [],
    rule_modes: []
  });
  dialogVisible.value = true;
}

function openEdit(row: AlarmChannelItem) {
  editingId.value = row.id;
  const rr = row.routing_rules ?? {};
  Object.assign(form, {
    name: row.name,
    channel_type: (row.channel_type || "webhook") as any,
    url: row.config?.url ?? "",
    webhook_url: row.config?.webhook_url ?? row.config?.url ?? "",
    trigger_url: row.config?.trigger_url ?? "",
    reset_url: row.config?.reset_url ?? "",
    auto_reset_seconds: Number(row.config?.auto_reset_seconds ?? 60),
    secret: row.config?.secret ?? "",
    timeout_ms: Number(row.config?.timeout_ms ?? 5000),
    include_evidence_urls: row.config?.include_evidence_urls ?? true,
    enabled: row.enabled,
    auto_route: Boolean(rr.auto_route),
    severities: Array.isArray(rr.severities) ? [...rr.severities] : [],
    zone_ids: Array.isArray(rr.zone_ids) ? [...rr.zone_ids] : [],
    rule_modes: Array.isArray(rr.rule_modes) ? [...rr.rule_modes] : []
  });
  dialogVisible.value = true;
}

function buildConfig(): Record<string, any> {
  if (form.channel_type === "wecom") {
    return {
      webhook_url: form.webhook_url.trim(),
      timeout_ms: form.timeout_ms
    };
  }
  if (form.channel_type === "dingtalk") {
    return {
      webhook_url: form.webhook_url.trim(),
      secret: form.secret.trim(),
      timeout_ms: form.timeout_ms
    };
  }
  if (form.channel_type === "sound_light") {
    return {
      trigger_url: form.trigger_url.trim(),
      reset_url: form.reset_url.trim(),
      method: "POST",
      auto_reset_seconds: form.auto_reset_seconds,
      timeout_ms: form.timeout_ms
    };
  }
  return {
    url: form.url.trim(),
    method: "POST",
    secret: form.secret.trim(),
    timeout_ms: form.timeout_ms,
    include_evidence_urls: form.include_evidence_urls
  };
}

async function submit() {
  const payload = {
    name: form.name.trim(),
    channel_type: form.channel_type,
    config: buildConfig(),
    routing_rules: {
      auto_route: form.auto_route,
      severities: form.severities,
      zone_ids: form.zone_ids,
      rule_modes: form.rule_modes
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

async function onReset(id: string) {
  await resetChannel(id);
  ElMessage.success(t("aegis.channels.resetQueued"));
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
        <el-table-column prop="name" :label="t('aegis.channels.name')" min-width="150" />
        <el-table-column :label="t('aegis.channels.type')" width="150">
          <template #default="{ row }">
            <el-tag size="small" :type="row.channel_type === 'sound_light' ? 'danger' : 'primary'">
              {{ typeLabel(row.channel_type) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('aegis.channels.url')" min-width="220" show-overflow-tooltip>
          <template #default="{ row }">{{ targetUrlOf(row) }}</template>
        </el-table-column>
        <el-table-column :label="t('aegis.channels.routing')" width="140">
          <template #default="{ row }">
            <el-tag v-if="row.routing_rules?.auto_route" size="small" type="success">
              Auto-Route
            </el-tag>
            <span v-else class="text-xs text-slate-400">Manual Rule</span>
          </template>
        </el-table-column>
        <el-table-column :label="t('aegis.common.enabled')" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
              {{ row.enabled ? t("aegis.common.enabled") : t("aegis.common.disabled") }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('aegis.common.actions')" width="280">
          <template #default="{ row }">
            <el-button link type="success" @click="onTest(row.id)">
              {{ t("aegis.channels.test") }}
            </el-button>
            <el-button
              v-if="row.channel_type === 'sound_light'"
              link
              type="warning"
              @click="onReset(row.id)"
            >
              {{ t("aegis.channels.reset") }}
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

    <el-dialog
      v-model="dialogVisible"
      :title="editingId ? t('aegis.common.edit') : t('aegis.common.create')"
      width="620px"
    >
      <el-form label-width="150px">
        <el-form-item :label="t('aegis.channels.name')">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item :label="t('aegis.channels.type')">
          <el-select v-model="form.channel_type" class="w-full">
            <el-option :label="t('aegis.channels.typeWebhook')" value="webhook" />
            <el-option :label="t('aegis.channels.typeWecom')" value="wecom" />
            <el-option :label="t('aegis.channels.typeDingtalk')" value="dingtalk" />
            <el-option :label="t('aegis.channels.typeSoundLight')" value="sound_light" />
          </el-select>
        </el-form-item>

        <template v-if="form.channel_type === 'webhook'">
          <el-form-item :label="t('aegis.channels.url')">
            <el-input v-model="form.url" placeholder="http://192.168.x.x:9000/hook" />
          </el-form-item>
          <el-form-item :label="t('aegis.channels.secret')">
            <el-input v-model="form.secret" show-password />
          </el-form-item>
        </template>

        <template v-else-if="form.channel_type === 'wecom'">
          <el-form-item :label="t('aegis.channels.url')">
            <el-input v-model="form.webhook_url" placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..." />
          </el-form-item>
        </template>

        <template v-else-if="form.channel_type === 'dingtalk'">
          <el-form-item :label="t('aegis.channels.url')">
            <el-input v-model="form.webhook_url" placeholder="https://oapi.dingtalk.com/robot/send?access_token=..." />
          </el-form-item>
          <el-form-item :label="t('aegis.channels.secret')">
            <el-input v-model="form.secret" show-password placeholder="SEC..." />
          </el-form-item>
        </template>

        <template v-else-if="form.channel_type === 'sound_light'">
          <el-form-item :label="t('aegis.channels.triggerUrl')">
            <el-input v-model="form.trigger_url" placeholder="http://192.168.10.50/relay/on" />
          </el-form-item>
          <el-form-item :label="t('aegis.channels.resetUrl')">
            <el-input v-model="form.reset_url" placeholder="http://192.168.10.50/relay/off" />
          </el-form-item>
          <el-form-item :label="t('aegis.channels.autoResetSeconds')">
            <el-input-number v-model="form.auto_reset_seconds" :min="0" :max="600" />
          </el-form-item>
        </template>

        <el-form-item :label="t('aegis.channels.timeoutMs')">
          <el-input-number v-model="form.timeout_ms" :min="500" :max="30000" />
        </el-form-item>

        <el-divider content-position="left">{{ t("aegis.channels.routing") }}</el-divider>
        <el-form-item :label="t('aegis.channels.autoRoute')">
          <el-switch v-model="form.auto_route" />
          <div class="text-xs text-slate-400 leading-5 mt-1">
            {{ t("aegis.channels.autoRouteHint") }}
          </div>
        </el-form-item>

        <template v-if="form.auto_route">
          <el-form-item :label="t('aegis.channels.severities')">
            <el-select v-model="form.severities" multiple class="w-full">
              <el-option label="critical" value="critical" />
              <el-option label="warning" value="warning" />
              <el-option label="info" value="info" />
            </el-select>
          </el-form-item>
          <el-form-item :label="t('aegis.channels.zones')">
            <el-tree-select
              v-model="form.zone_ids"
              :data="zones"
              :props="{ label: 'name', value: 'id', children: 'children' }"
              multiple
              check-strictly
              show-checkbox
              class="w-full"
            />
          </el-form-item>
          <el-form-item :label="t('aegis.channels.ruleModes')">
            <el-select v-model="form.rule_modes" multiple class="w-full">
              <el-option label="anomaly_detect" value="anomaly_detect" />
              <el-option label="count_threshold" value="count_threshold" />
              <el-option label="absence_detect" value="absence_detect" />
              <el-option label="sop_sequence" value="sop_sequence" />
            </el-select>
          </el-form-item>
        </template>

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
