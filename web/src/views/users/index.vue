<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  createUser,
  deleteUser,
  getUserZones,
  getZonesTree,
  listUsers,
  resetUserPassword,
  updateUser,
  updateUserZones,
  type UserItem,
  type ZoneTreeNode
} from "@/api/aegis";
import { formatDateTime } from "@/utils/format";
import { isCancelError, showError, showSuccess } from "@/utils/error";
import { formatUserRole } from "@/utils/aegisLabels";

defineOptions({ name: "AegisUsers" });

const { t } = useI18n();
const loading = ref(false);
const loadError = ref("");
const saving = ref(false);
const savingScope = ref(false);
const users = ref<UserItem[]>([]);
const page = ref(1);
const pageSize = ref(20);
const pagedUsers = computed(() => {
  const start = (page.value - 1) * pageSize.value;
  return users.value.slice(start, start + pageSize.value);
});
const zones = ref<ZoneTreeNode[]>([]);
const dialogVisible = ref(false);
const editingId = ref<string | null>(null);

const scopeDialogVisible = ref(false);
const scopeUser = ref<UserItem | null>(null);
const scopeAllZones = ref(true);
const scopeCheckedIds = ref<string[]>([]);
const zoneTreeRef = ref<any>(null);

const form = reactive({
  username: "",
  password: "",
  display_name: "",
  role: "viewer" as "admin" | "operator" | "viewer",
  enabled: true
});

function roleTagType(role: string): "danger" | "warning" | "info" {
  if (role === "admin") return "danger";
  if (role === "operator") return "warning";
  return "info";
}

async function loadData() {
  loading.value = true;
  try {
    const [uRes, zRes] = await Promise.all([listUsers(), getZonesTree().catch(() => ({ data: [] }))]);
    users.value = uRes?.data ?? [];
    zones.value = zRes?.data ?? [];
    loadError.value = "";
  } catch (err: any) {
    loadError.value = showError(err, t("aegis.common.loadFailed"));
  } finally {
    loading.value = false;
  }
}

function openCreate() {
  editingId.value = null;
  Object.assign(form, {
    username: "",
    password: "",
    display_name: "",
    role: "viewer",
    enabled: true
  });
  dialogVisible.value = true;
}

function openEdit(row: UserItem) {
  editingId.value = row.id;
  Object.assign(form, {
    username: row.username,
    password: "",
    display_name: row.display_name,
    role: row.role,
    enabled: row.enabled
  });
  dialogVisible.value = true;
}

async function openScopeDialog(row: UserItem) {
  try {
    scopeUser.value = row;
    const res = await getUserZones(row.id);
    scopeAllZones.value = res?.data?.all_zones_access ?? true;
    scopeCheckedIds.value = res?.data?.zone_ids ?? [];
    scopeDialogVisible.value = true;
    await nextTick();
    if (zoneTreeRef.value?.setCheckedKeys) {
      zoneTreeRef.value.setCheckedKeys(scopeCheckedIds.value);
    }
  } catch (err: any) {
    showError(err);
  }
}

async function submitScope() {
  if (!scopeUser.value) return;
  savingScope.value = true;
  try {
    let selectedIds: string[] = [];
    if (!scopeAllZones.value && zoneTreeRef.value?.getCheckedKeys) {
      selectedIds = zoneTreeRef.value.getCheckedKeys(false) as string[];
    }
    await updateUserZones(scopeUser.value.id, {
      all_zones_access: scopeAllZones.value,
      zone_ids: selectedIds
    });
    scopeDialogVisible.value = false;
    showSuccess(t("aegis.users.scopeSaved"));
    await loadData();
  } catch (err: any) {
    showError(err);
  } finally {
    savingScope.value = false;
  }
}

async function submit() {
  if (!form.display_name.trim() || (!editingId.value && !form.username.trim())) {
    ElMessage.warning(t("aegis.common.validationRequired"));
    return;
  }
  if (!editingId.value && form.password.length < 8) {
    ElMessage.warning(t("aegis.common.passwordMinLength"));
    return;
  }
  saving.value = true;
  try {
    if (editingId.value) {
      await updateUser(editingId.value, {
        display_name: form.display_name.trim(),
        role: form.role,
        enabled: form.enabled
      });
    } else {
      await createUser({
        ...form,
        username: form.username.trim(),
        display_name: form.display_name.trim()
      });
    }
    dialogVisible.value = false;
    showSuccess(t("aegis.common.saveSuccess"));
    await loadData();
  } catch (err: any) {
    showError(err);
  } finally {
    saving.value = false;
  }
}

async function onResetPassword(row: UserItem) {
  try {
    const { value } = await ElMessageBox.prompt(
      t("aegis.users.resetPassword"),
      row.username,
      {
        inputType: "password",
        inputValidator: (val: string) =>
          val && val.length >= 8 ? true : t("aegis.common.passwordMinLength")
      }
    );
    if (value) {
      await resetUserPassword(row.id, value);
      showSuccess(t("aegis.common.saveSuccess"));
    }
  } catch (err: any) {
    if (!isCancelError(err)) {
      showError(err);
    }
  }
}

async function onDelete(id: string) {
  try {
    await ElMessageBox.confirm(
      t("aegis.common.confirmDelete"),
      t("aegis.common.confirmTitle"),
      { type: "warning" }
    );
    await deleteUser(id);
    showSuccess(t("aegis.common.deleteSuccess"));
    await loadData();
  } catch (err: any) {
    if (!isCancelError(err)) {
      showError(err);
    }
  }
}

onMounted(loadData);
</script>

<template>
  <div class="aegis-page aegis-page--fixed">
    <section class="aegis-panel aegis-panel--fill">
      <header class="aegis-page-header">
        <div class="aegis-page-title-group">
          <h1 class="aegis-page-title">{{ t("menus.aegisUsers") }}</h1>
        </div>
        <div class="aegis-header-actions">
          <el-button type="primary" @click="openCreate">{{ t("aegis.common.create") }}</el-button>
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

      <div class="aegis-table-wrap aegis-table-wrap--fill">
        <el-table v-loading="loading" :data="pagedUsers" height="100%" border stripe>
          <el-table-column prop="username" :label="t('aegis.users.username')" width="145" show-overflow-tooltip>
            <template #default="{ row }">
              <span class="aegis-mono-id font-medium">{{ row.username }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="display_name" :label="t('aegis.users.displayName')" min-width="145" show-overflow-tooltip />
          <el-table-column :label="t('aegis.users.role')" width="116">
            <template #default="{ row }">
              <el-tag size="small" :type="roleTagType(row.role)">
                {{ formatUserRole(row.role, t) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.users.workshopScope')" width="148">
            <template #default="{ row }">
              <el-tag
                size="small"
                :type="row.role === 'admin' || row.all_zones_access !== false ? 'success' : 'warning'"
                effect="plain"
              >
                {{ row.role === "admin" || row.all_zones_access !== false ? t("aegis.users.allZonesAccess") : t("aegis.users.assignedZones") }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.common.enabled')" width="88">
            <template #default="{ row }">
              <el-tag size="small" :type="row.enabled ? 'success' : 'danger'">
                {{ row.enabled ? t("aegis.common.enabled") : t("aegis.common.disabled") }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.users.lastLogin')" width="168">
            <template #default="{ row }">
              <span class="aegis-tabular">{{ formatDateTime(row.last_login_at) }}</span>
            </template>
          </el-table-column>
          <el-table-column :label="t('aegis.common.actions')" width="295" fixed="right">
            <template #default="{ row }">
              <el-button link type="primary" @click="openEdit(row)">
                {{ t("aegis.common.edit") }}
              </el-button>
              <el-button
                v-if="row.role !== 'admin'"
                link
                type="success"
                @click="openScopeDialog(row)"
              >
                {{ t("aegis.users.workshopScope") }}
              </el-button>
              <el-button link type="warning" @click="onResetPassword(row)">
                {{ t("aegis.users.resetPassword") }}
              </el-button>
              <el-button link type="danger" @click="onDelete(row.id)">
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
          :total="users.length"
          layout="total, sizes, prev, pager, next"
          @size-change="page = 1"
        />
      </div>
    </section>

    <el-dialog v-model="dialogVisible" :title="editingId ? t('aegis.common.edit') : t('aegis.common.create')" width="480px">
      <el-form label-width="120px" size="small">
        <el-form-item :label="t('aegis.users.username')">
          <el-input v-model="form.username" :disabled="Boolean(editingId)" />
        </el-form-item>
        <el-form-item v-if="!editingId" :label="t('aegis.users.password')">
          <el-input v-model="form.password" show-password />
        </el-form-item>
        <el-form-item :label="t('aegis.users.displayName')">
          <el-input v-model="form.display_name" />
        </el-form-item>
        <el-form-item :label="t('aegis.users.role')">
          <el-select v-model="form.role" class="w-full">
            <el-option :label="formatUserRole('admin', t)" value="admin" />
            <el-option :label="formatUserRole('operator', t)" value="operator" />
            <el-option :label="formatUserRole('viewer', t)" value="viewer" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('aegis.common.enabled')">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" :loading="saving" @click="submit">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>

    <el-dialog
      v-model="scopeDialogVisible"
      :title="`${t('aegis.users.workshopScope')} — ${scopeUser?.username ?? ''}`"
      width="520px"
    >
      <div class="space-y-4">
        <div class="space-y-2">
          <el-switch v-model="scopeAllZones" :active-text="t('aegis.users.allZonesAccess')" />
          <el-alert
            type="info"
            show-icon
            :closable="false"
            :title="t('aegis.users.allZonesHint')"
          />
        </div>
        <div v-show="!scopeAllZones" class="border rounded p-3">
          <div class="text-xs font-medium mb-2 text-slate-600">{{ t("aegis.users.assignedZones") }}</div>
          <el-scrollbar max-height="280px">
            <el-tree
              ref="zoneTreeRef"
              :data="zones"
              node-key="id"
              :default-checked-keys="scopeCheckedIds"
              :props="{ label: 'name', children: 'children' }"
              show-checkbox
              check-strictly
              default-expand-all
            />
          </el-scrollbar>
        </div>
      </div>
      <template #footer>
        <el-button @click="scopeDialogVisible = false">{{ t("aegis.common.cancel") }}</el-button>
        <el-button type="primary" :loading="savingScope" @click="submitScope">{{ t("aegis.common.save") }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>
