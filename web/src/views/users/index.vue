<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  createUser,
  deleteUser,
  listUsers,
  resetUserPassword,
  updateUser,
  type UserItem
} from "@/api/aegis";

defineOptions({ name: "AegisUsers" });

const { t } = useI18n();
const loading = ref(false);
const users = ref<UserItem[]>([]);
const dialogVisible = ref(false);
const editingId = ref<string | null>(null);

const form = reactive({
  username: "",
  password: "",
  display_name: "",
  role: "viewer" as "admin" | "operator" | "viewer",
  enabled: true
});

async function loadData() {
  loading.value = true;
  try {
    const res = await listUsers();
    users.value = res?.data ?? [];
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

async function submit() {
  if (editingId.value) {
    await updateUser(editingId.value, {
      display_name: form.display_name,
      role: form.role,
      enabled: form.enabled
    });
  } else {
    await createUser({ ...form });
  }
  dialogVisible.value = false;
  ElMessage.success("OK");
  await loadData();
}

async function onResetPassword(row: UserItem) {
  const { value } = await ElMessageBox.prompt(
    t("aegis.users.resetPassword"),
    row.username,
    { inputType: "password" }
  );
  if (value) {
    await resetUserPassword(row.id, value);
    ElMessage.success("Password updated");
  }
}

async function onDelete(id: string) {
  await ElMessageBox.confirm(t("aegis.common.confirmDelete"));
  await deleteUser(id);
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

      <el-table v-loading="loading" :data="users" border stripe>
        <el-table-column prop="username" :label="t('aegis.users.username')" width="150" />
        <el-table-column prop="display_name" :label="t('aegis.users.displayName')" min-width="160" />
        <el-table-column prop="role" :label="t('aegis.users.role')" width="120" />
        <el-table-column prop="enabled" :label="t('aegis.common.enabled')" width="100" />
        <el-table-column prop="last_login_at" label="Last Login" width="190" />
        <el-table-column :label="t('aegis.common.actions')" width="240">
          <template #default="{ row }">
            <el-button link type="primary" @click="openEdit(row)">
              {{ t("aegis.common.edit") }}
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
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? t('aegis.common.edit') : t('aegis.common.create')" width="480px">
      <el-form label-width="120px">
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
            <el-option label="admin" value="admin" />
            <el-option label="operator" value="operator" />
            <el-option label="viewer" value="viewer" />
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
