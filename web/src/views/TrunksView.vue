<script setup lang="ts">
// TrunksView — per-tenant SIP trunk management. Two auth modes covered:
//   register=true  (aggregator-style: user/pass/realm)
//   register=false (Tata IP-auth style: proxy IP + SBC CIDR)
// Saving any trunk triggers a server-side FreeSWITCH gateway re-render
// + sofia profile rescan, so changes are live without restarting FS.
// The list auto-refreshes every 10s so the online/offline badge stays
// current as gateways register / time out.
import { ref, computed } from "vue";
import { errMessage } from "../lib/errors";
import { useList } from "../composables/useList";
import { usePoll } from "../composables/usePoll";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import { trunkStatusTone } from "../lib/status";
import type { Trunk } from "../api/types";
import {
  Button,
  IconButton,
  Panel,
  DataTable,
  type Column,
  Badge,
  Modal,
  TextField,
  Select,
  Toggle,
} from "../components/ui";
import { Pencil, Trash2 } from "lucide-vue-next";
import { confirmDialog } from "../lib/confirm";

// status string → human label for the status badge.
const statusLabels: Record<string, string> = {
  online: "online",
  offline: "offline",
  connecting: "connecting",
  not_loaded: "not loaded",
  unknown: "unknown",
};

// Registration state → plain words for the status line under the badge.
const regLabels: Record<string, string> = {
  REGED: "registered",
  TRYING: "registering…",
  REGISTER: "registering…",
  NOREG: "not registered",
  UNREGED: "unregistered",
  UNREGISTER: "unregistering…",
  FAILED: "registration failed",
  FAIL_WAIT: "registration failed, retrying",
  REG_FAIL: "registration failed",
  NOAUTH: "credentials rejected",
  EXPIRED: "registration expired",
};

// One readable line: how the trunk authenticates, its registration state
// (register mode) and the last keepalive round trip.
function stateLine(t: Trunk): string {
  if (!t.reg_state) return "not loaded in FreeSWITCH";
  const ping = t.ping_ms ? ` · ping ${t.ping_ms.toFixed(1)} ms` : " · no ping reply";
  if (!t.register) return `IP-auth${ping}`;
  const label = regLabels[t.reg_state];
  const state = label ? (t.reg_state === "REGED" ? label : `${label} (${t.reg_state})`) : t.reg_state;
  return `${state}${ping}`;
}

// "12 min" / "3 h" / "2 d" since an ISO time — how long the counters cover.
function since(iso: string): string {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return "<1 min";
  if (s < 3600) return `${Math.floor(s / 60)} min`;
  if (s < 86400) return `${Math.floor(s / 3600)} h`;
  return `${Math.floor(s / 86400)} d`;
}

// Whether calls actually work — keepalives alone can't tell. Counts are
// FreeSWITCH's since it (re)loaded the trunk.
function callsLine(t: Trunk): string {
  const c = t.calls;
  if (!c) return "";
  const out = c.out ? `calls out: ${c.out} tried, ${c.failed_out} failed` : "calls out: none yet";
  const inb = c.in === undefined ? "" : ` · in: ${c.in}${c.failed_in ? ` (${c.failed_in} failed)` : ""}`;
  return `${out}${inb} · last ${since(c.since)}`;
}

// Every outbound attempt failed: reachable or not, calls aren't working.
function callsFailing(t: Trunk): boolean {
  return !!t.calls && t.calls.out > 0 && t.calls.failed_out === t.calls.out;
}

const { items: trunks, loading, load } = useList<Trunk>("/v1/trunks");
const reloadingFS = ref(false);

const cols: Column[] = [
  { key: "name", label: "Name" },
  { key: "provider_type", label: "Provider", mono: true },
  { key: "status", label: "Status" },
  { key: "auth", label: "Auth" },
  { key: "endpoint", label: "Endpoint", mono: true },
  { key: "active", label: "Active" },
];

const showForm = ref(false);
const editing = ref<Trunk | null>(null);
const saving = ref(false);

const form = ref({
  name: "",
  provider_type: "generic",
  register: false,
  sip_username: "",
  sip_password: "",
  sip_realm: "",
  proxy_ip: "",
  auth_acl_cidr: "",
  from_domain: "",
  auth_username: "",
  active: true,
});
// The edited trunk's other extra_params keys, carried through save untouched.
const extraParams = ref<Record<string, unknown>>({});

const providerTypes = ["generic", "airtel", "jio", "tata"];
const providerOptions = providerTypes.map((p) => ({ value: p, label: p }));

const formTitle = computed(() => (editing.value ? "Edit trunk" : "New trunk"));


function newTrunk() {
  editing.value = null;
  form.value = {
    name: "",
    provider_type: "generic",
    register: false,
    sip_username: "",
    sip_password: "",
    sip_realm: "",
    proxy_ip: "",
    auth_acl_cidr: "",
    from_domain: "",
    auth_username: "",
    active: true,
  };
  extraParams.value = {};
  showForm.value = true;
}

async function startEdit(t: Trunk) {
  editing.value = t;
  // Fetch the full trunk (list response omits sip_password).
  const full = await api.get<Trunk>(`/v1/trunks/${t.id}`);
  form.value = {
    name: full.name,
    provider_type: full.provider_type,
    register: full.register,
    sip_username: full.sip_username,
    sip_password: full.sip_password || "",
    sip_realm: full.sip_realm,
    proxy_ip: full.proxy_ip,
    auth_acl_cidr: full.auth_acl_cidr,
    from_domain: typeof full.extra_params?.from_domain === "string" ? full.extra_params.from_domain : "",
    auth_username: typeof full.extra_params?.auth_username === "string" ? full.extra_params.auth_username : "",
    active: full.active,
  };
  extraParams.value = { ...(full.extra_params || {}) };
  showForm.value = true;
}

async function save() {
  saving.value = true;
  try {
    const { from_domain, auth_username, ...fields } = form.value;
    const extra_params = { ...extraParams.value };
    for (const [k, v] of Object.entries({ from_domain, auth_username })) {
      if (v.trim()) extra_params[k] = v.trim();
      else delete extra_params[k];
    }
    const body = { ...fields, extra_params };
    if (editing.value) {
      await api.patch<Trunk>(`/v1/trunks/${editing.value.id}`, body);
    } else {
      await api.post<Trunk>("/v1/trunks", body);
    }
    showForm.value = false;
    editing.value = null;
    await load();
    toast.success("Trunk saved");
  } catch (e) {
    toast.error(errMessage(e, "save failed"));
  } finally {
    saving.value = false;
  }
}

async function removeTrunk(t: Trunk) {
  if (!(await confirmDialog(`Delete trunk "${t.name}"?`))) return;
  try {
    await api.del(`/v1/trunks/${t.id}`);
    await load();
    toast.success("Trunk deleted");
  } catch (e) {
    toast.error(errMessage(e, "delete failed"));
  }
}

async function reloadFS() {
  reloadingFS.value = true;
  try {
    await api.post("/v1/trunks/reload");
    toast.success("FS gateway dir + sofia rescan triggered.");
  } catch (e) {
    toast.error(errMessage(e, "reload failed"));
  } finally {
    reloadingFS.value = false;
  }
}

// useList loads on mount; re-poll silently every 10s for live trunk status.
usePoll(() => load({ silent: true }), 10000, { immediate: false });
</script>

<template>
  <div class="view">
    <div class="row-between">
      <h1 class="page-title">Trunks</h1>
      <div class="hstack">
        <Button
          variant="ghost"
          :loading="reloadingFS"
          title="Re-render gateway XML and rescan sofia"
          @click="reloadFS"
        >
          Reload FS
        </Button>
        <Button @click="newTrunk">New trunk</Button>
      </div>
    </div>

    <Panel>
      <DataTable :columns="cols" :rows="trunks" :loading="loading">
        <template #cell-status="{ row }">
          <span :title="row.state_raw || '(no FS state)'">
            <Badge :tone="trunkStatusTone(row.status)">
              {{ statusLabels[row.status || "unknown"] || row.status }}
            </Badge>
          </span>
          <Badge
            v-if="callsFailing(row)"
            tone="danger"
            style="margin-left: 0.35rem"
            title="Every outbound call through this trunk has failed since it was loaded"
          >
            calls failing
          </Badge>
          <div class="trunk-state">{{ stateLine(row) }}</div>
          <div v-if="row.calls" class="trunk-state">{{ callsLine(row) }}</div>
        </template>
        <template #cell-auth="{ row }">
          <Badge :tone="row.register ? 'accent' : 'warning'">
            {{ row.register ? "REGISTER" : "IP-auth" }}
          </Badge>
        </template>
        <template #cell-endpoint="{ row }">
          {{ row.register ? `${row.sip_username}@${row.sip_realm || row.proxy_ip}${row.sip_realm && row.proxy_ip ? ` via ${row.proxy_ip}` : ""}` : row.proxy_ip || "—" }}
        </template>
        <template #cell-active="{ row }">
          <Badge :tone="row.active ? 'success' : 'neutral'">{{ row.active ? "yes" : "no" }}</Badge>
        </template>
        <template #row-actions="{ row }">
          <IconButton title="Edit" variant="ghost" size="sm" @click="startEdit(row)">
            <Pencil :size="16" />
          </IconButton>
          <IconButton title="Delete" variant="danger" size="sm" @click="removeTrunk(row)">
            <Trash2 :size="16" />
          </IconButton>
        </template>
        <template #empty>
          No trunks yet — click <span>New trunk</span> to add one.
        </template>
      </DataTable>
    </Panel>

    <p class="text-lighter">
      Saving a trunk renders gateway XML to
      <code class="mono">freeswitch/conf/sip_profiles/external/dynamic/</code>
      and runs
      <code class="mono">sofia profile external rescan</code>. Zero downtime;
      live calls stay up.
    </p>

    <Modal v-model:open="showForm" :title="formTitle" size="lg">
      <form id="trunk-form" class="vstack" @submit.prevent="save">
        <div class="grid grid-2">
          <TextField v-model="form.name" label="Name" required placeholder="Tata production" />
          <Select v-model="form.provider_type" label="Provider type" :options="providerOptions" />
        </div>

        <div class="hstack" style="gap: 1.5rem; padding-top: 0.5rem; border-top: 1px solid var(--border)">
          <Toggle v-model="form.register" label="REGISTER auth (aggregator-style)" />
          <Toggle v-model="form.active" label="Active" />
        </div>

        <!-- REGISTER auth fields -->
        <template v-if="form.register">
          <div class="grid grid-2">
            <TextField v-model="form.sip_username" label="SIP username" mono required />
            <TextField v-model="form.sip_password" label="SIP password" type="password" mono required />
          </div>
          <TextField
            v-model="form.sip_realm"
            label="SIP realm / domain (optional — defaults to registrar/proxy IP)"
            mono
            placeholder="sip.example.com"
          />
          <div class="grid grid-2">
            <TextField
              v-model="form.proxy_ip"
              label="Registrar / proxy IP (optional)"
              mono
              placeholder="10.0.0.42:5060"
              hint="Where REGISTER and calls go when the realm isn't resolvable (e.g. a carrier's IMS domain)."
            />
            <TextField
              v-model="form.auth_username"
              label="Auth username (optional)"
              mono
              hint="Digest user if different from the SIP username (IMS: private identity)."
            />
          </div>
          <TextField
            v-model="form.from_domain"
            label="From domain (optional)"
            mono
            hint="Host sent in From. Empty = SIP realm."
          />
        </template>

        <!-- IP-auth fields -->
        <template v-else>
          <div class="grid grid-2">
            <TextField
              v-model="form.proxy_ip"
              label="SBC proxy IP"
              mono
              required
              placeholder="10.0.0.42"
              hint="Tata's SBC IP — sent to and trusted from."
            />
            <TextField
              v-model="form.auth_acl_cidr"
              label="SBC ACL CIDR"
              mono
              placeholder="10.0.0.40/29"
              hint="For the inbound ACL."
            />
          </div>
          <div class="grid grid-2">
            <TextField
              v-model="form.sip_realm"
              label="SIP realm (optional — defaults to proxy IP)"
              mono
            />
            <TextField
              v-model="form.from_domain"
              label="From domain (optional)"
              mono
              placeholder="10.0.0.5"
              hint="Host sent in From — e.g. your circuit IP. Empty = SIP realm."
            />
          </div>
        </template>
      </form>
      <template #footer>
        <Button variant="ghost" @click="showForm = false; editing = null">Cancel</Button>
        <Button type="submit" form="trunk-form" :loading="saving">
          {{ editing ? "Save changes" : "Create" }}
        </Button>
      </template>
    </Modal>
  </div>
</template>

<style scoped>
.trunk-state {
  margin-top: 0.2rem;
  font-size: 0.75rem;
  color: var(--color-ink-muted);
  white-space: nowrap;
}
</style>
