<script setup lang="ts">
// TrunksView — per-tenant SIP trunk management. Two auth modes covered:
//   register=true  (Exotel/aggregator-style: user/pass/realm)
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

// status string → human label for the status badge.
const statusLabels: Record<string, string> = {
  online: "online",
  offline: "offline",
  connecting: "connecting",
  not_loaded: "not loaded",
  unknown: "unknown",
};

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
  active: true,
});
// The edited trunk's other extra_params keys, carried through save untouched.
const extraParams = ref<Record<string, unknown>>({});

const providerTypes = ["generic", "exotel", "airtel", "jio", "tata"];
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
    active: full.active,
  };
  extraParams.value = { ...(full.extra_params || {}) };
  showForm.value = true;
}

async function save() {
  saving.value = true;
  try {
    const { from_domain, ...fields } = form.value;
    const extra_params = { ...extraParams.value };
    if (from_domain.trim()) extra_params.from_domain = from_domain.trim();
    else delete extra_params.from_domain;
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
  if (!confirm(`Delete trunk "${t.name}"?`)) return;
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
        </template>
        <template #cell-auth="{ row }">
          <Badge :tone="row.register ? 'accent' : 'warning'">
            {{ row.register ? "REGISTER" : "IP-auth" }}
          </Badge>
        </template>
        <template #cell-endpoint="{ row }">
          {{ row.register ? `${row.sip_username}@${row.sip_realm}` : row.proxy_ip || "—" }}
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
            label="SIP realm"
            mono
            required
            placeholder="account.pstn.exotel.com"
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
