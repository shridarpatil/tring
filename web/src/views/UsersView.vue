<script setup lang="ts">
import { computed, ref } from "vue";
import { KeyRound, LogOut, Trash2 } from "lucide-vue-next";
import { errMessage } from "../lib/errors";
import { useList } from "../composables/useList";
import { usePoll } from "../composables/usePoll";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import { toast } from "../lib/toast";
import { confirmDialog } from "../lib/confirm";
import { agentSignalState } from "../lib/status";
import { roleOptions, validateLogin, validateNewUser } from "../lib/users";
import type { Agent, AgentToken } from "../api/types";
import {
  Button,
  Panel,
  DataTable,
  type Column,
  SignalDot,
  Badge,
  IconButton,
  Modal,
  Select,
  TextField,
  CopyButton,
} from "../components/ui";

interface UserRow {
  id: string;
  display_name: string;
  email: string;
  role: string;
  sip_username: string;
  available_for_calls: boolean;
  teams: { name: string; is_manager: boolean }[];
  can_login: boolean;
  created_at: string;
}

const { items: users, loading, load } = useList<UserRow>("/v1/users");
// Live presence is only on /v1/agents (same id as the user); poll it and
// merge by id so online/on-call tracks registrations.
const presence = ref<Record<string, Agent["status"]>>({});
async function loadPresence() {
  try {
    const list = await api.get<Agent[]>("/v1/agents");
    presence.value = Object.fromEntries(list.map((a) => [a.id, a.status]));
  } catch {
    // presence is best-effort; the table still works without it
  }
}
usePoll(
  async () => {
    await Promise.all([load({ silent: true }), loadPresence()]);
  },
  10_000,
  { immediate: true },
);

const selfId = computed(() => auth.state.user?.id ?? "");
const isSelf = (u: UserRow) => u.id === selfId.value;

const cols: Column[] = [
  { key: "status", label: "Status" },
  { key: "display_name", label: "Name" },
  { key: "email", label: "Email" },
  { key: "role", label: "Role" },
  { key: "sip_username", label: "SIP username", mono: true },
  { key: "teams", label: "Teams" },
  { key: "can_login", label: "Login" },
  { key: "available", label: "Availability" },
];

// --- create ---
const showForm = ref(false);
const blank = () => ({ email: "", password: "", role: "agent", display_name: "", sip_username: "" });
const form = ref(blank());
const saving = ref(false);

async function create() {
  const problem = validateNewUser(form.value);
  if (problem) return toast.error(problem);
  saving.value = true;
  try {
    const body: Record<string, string> = { ...form.value };
    if (!body.sip_username.trim()) delete body.sip_username;
    await api.post("/v1/users", body);
    form.value = blank();
    showForm.value = false;
    await load();
    toast.success("User created");
  } catch (e) {
    toast.error(errMessage(e, "create failed"));
  } finally {
    saving.value = false;
  }
}

async function patch(u: UserRow, body: Record<string, string>, okMsg: string) {
  try {
    const updated = await api.patch<UserRow>(`/v1/users/${u.id}`, body);
    const i = users.value.findIndex((x) => x.id === u.id);
    if (i >= 0) users.value[i] = updated;
    toast.success(okMsg);
    return true;
  } catch (e) {
    toast.error(errMessage(e, "update failed"));
    await load({ silent: true }); // resync a role select that already moved
    return false;
  }
}

const setRole = (u: UserRow, role: string) => patch(u, { role }, "Role updated");

// --- set login / reset password ---
const loginFor = ref<UserRow | null>(null);
const loginForm = ref({ email: "", password: "" });
const showLogin = computed({
  get: () => loginFor.value !== null,
  set: (v: boolean) => {
    if (!v) loginFor.value = null;
  },
});
function openLogin(u: UserRow) {
  loginFor.value = u;
  loginForm.value = { email: u.email, password: "" };
}
async function saveLogin() {
  const u = loginFor.value;
  if (!u) return;
  const problem = validateLogin(loginForm.value.email, loginForm.value.password);
  if (problem) return toast.error(problem);
  saving.value = true;
  const done = await patch(u, { ...loginForm.value }, u.can_login ? "Password reset" : "Login set");
  saving.value = false;
  if (done) loginFor.value = null;
}

async function clearLogin(u: UserRow) {
  if (!(await confirmDialog(`Remove the console login for ${u.email}? They keep their SIP identity.`, { confirmLabel: "Remove login" }))) return;
  await patch(u, { email: "" }, "Login removed");
}

async function remove(u: UserRow) {
  if (!(await confirmDialog(`Delete ${u.display_name || u.sip_username}? This cannot be undone.`))) return;
  try {
    await api.del(`/v1/users/${u.id}`);
    users.value = users.value.filter((x) => x.id !== u.id);
    toast.success("User deleted");
  } catch (e) {
    toast.error(errMessage(e, "delete failed"));
  }
}

// Same endpoint the integrator's backend uses; an operator can also flip it here.
async function setAvailability(u: UserRow, available: boolean) {
  try {
    await api.post(`/v1/agents/${u.id}/availability`, { available });
    u.available_for_calls = available;
  } catch (e) {
    toast.error(errMessage(e, "update failed"));
  }
}

const tokenFor = ref("");
const token = ref<AgentToken | null>(null);
async function mintToken(u: UserRow) {
  tokenFor.value = u.sip_username;
  token.value = null;
  try {
    token.value = await api.get<AgentToken>(`/v1/agents/${u.id}/token`);
  } catch (e) {
    toast.error(errMessage(e, "token failed"));
  }
}
</script>

<template>
  <div class="view">
    <div class="row-between">
      <h1 class="page-title">Users</h1>
      <Button @click="showForm = true">New user</Button>
    </div>

    <div v-if="token" role="alert" data-variant="warning">
      <div class="hstack justify-between">
        <div>
          Token for <span class="mono">{{ tokenFor }}</span>
          <span class="text-light">
            · expires {{ new Date(token.expires_at).toLocaleTimeString() }}</span
          >
        </div>
        <CopyButton :value="token.token" label="Copy token" />
      </div>
      <code class="mono reveal-code">{{ token.token }}</code>
    </div>

    <Panel>
      <DataTable
        :columns="cols"
        :rows="users"
        :loading="loading"
        empty-text="No users yet"
        max-height="calc(100vh - 16rem)"
      >
        <template #cell-status="{ row }">
          <SignalDot :state="agentSignalState(presence[row.id])" />
        </template>
        <template #cell-display_name="{ row }">
          {{ row.display_name || "—" }}<span v-if="isSelf(row)" class="text-light"> (you)</span>
        </template>
        <template #cell-email="{ row }">
          <span :class="{ 'text-light': !row.email }">{{ row.email || "—" }}</span>
        </template>
        <template #cell-role="{ row }">
          <Select
            :model-value="row.role"
            :options="roleOptions"
            :disabled="isSelf(row)"
            @update:model-value="(v: string) => setRole(row, v)"
          />
        </template>
        <template #cell-teams="{ row }">
          <span v-if="!row.teams.length" class="text-light">—</span>
          <span v-else class="hstack">
            <Badge v-for="t in row.teams" :key="t.name">{{ t.name }}{{ t.is_manager ? " (manager)" : "" }}</Badge>
          </span>
        </template>
        <template #cell-can_login="{ row }">
          <Badge :tone="row.can_login ? 'success' : 'warning'">{{ row.can_login ? "Can log in" : "No login" }}</Badge>
        </template>
        <template #cell-available="{ row }">
          <button
            type="button"
            class="plain"
            :title="row.available_for_calls ? 'Click to set Away' : 'Click to set Available'"
            @click="setAvailability(row, !row.available_for_calls)"
          >
            <Badge :tone="row.available_for_calls ? 'success' : 'warning'">
              {{ row.available_for_calls ? "Available" : "Away" }}
            </Badge>
          </button>
        </template>
        <template #row-actions="{ row }">
          <div class="hstack">
            <Button variant="secondary" size="sm" @click="mintToken(row)">Get token</Button>
            <IconButton :title="row.can_login ? 'Reset password' : 'Set login'" size="sm" @click="openLogin(row)">
              <KeyRound :size="14" />
            </IconButton>
            <IconButton title="Clear login" size="sm" :disabled="isSelf(row) || !row.can_login" @click="clearLogin(row)">
              <LogOut :size="14" />
            </IconButton>
            <IconButton title="Delete" size="sm" :disabled="isSelf(row)" @click="remove(row)">
              <Trash2 :size="14" />
            </IconButton>
          </div>
        </template>
      </DataTable>
    </Panel>

    <Modal v-model:open="showForm" title="New user">
      <form id="user-form" class="vstack" @submit.prevent="create">
        <TextField v-model="form.email" label="Email" type="email" required placeholder="alice@example.com" autocomplete="off" />
        <TextField v-model="form.display_name" label="Display name" placeholder="Alice" />
        <Select v-model="form.role" label="Role" :options="roleOptions" required />
        <TextField v-model="form.password" label="Password" type="password" required hint="At least 8 characters" autocomplete="new-password" />
        <TextField v-model="form.sip_username" label="SIP username" mono hint="Optional; defaults to the email" />
      </form>
      <template #footer>
        <Button variant="ghost" @click="showForm = false">Cancel</Button>
        <Button type="submit" form="user-form" :loading="saving">Create</Button>
      </template>
    </Modal>

    <Modal v-model:open="showLogin" :title="loginFor?.can_login ? 'Reset password' : 'Set login'">
      <form id="login-form" class="vstack" @submit.prevent="saveLogin">
        <TextField v-model="loginForm.email" label="Email" type="email" required autocomplete="off" />
        <TextField v-model="loginForm.password" label="New password" type="password" required hint="At least 8 characters" autocomplete="new-password" />
      </form>
      <template #footer>
        <Button variant="ghost" @click="showLogin = false">Cancel</Button>
        <Button type="submit" form="login-form" :loading="saving">Save</Button>
      </template>
    </Modal>
  </div>
</template>
