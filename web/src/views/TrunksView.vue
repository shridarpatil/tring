<script setup lang="ts">
// TrunksView — per-tenant SIP trunk management. Two auth modes covered:
//   register=true  (Exotel/aggregator-style: user/pass/realm)
//   register=false (Tata IP-auth style: proxy IP + SBC CIDR)
// Saving any trunk triggers a server-side FreeSWITCH gateway re-render
// + sofia profile rescan, so changes are live without restarting FS.
// The list auto-refreshes every 10s so the online/offline badge stays
// current as gateways register / time out.
import { ref, onMounted, onUnmounted, computed } from "vue";
import { api } from "../api/client";
import type { Trunk } from "../api/types";

const statusStyle: Record<string, { dot: string; label: string; text: string }> = {
  online:      { dot: "bg-green-500",  text: "text-green-700",  label: "online" },
  offline:     { dot: "bg-red-500",    text: "text-red-700",    label: "offline" },
  connecting:  { dot: "bg-amber-500",  text: "text-amber-700",  label: "connecting" },
  not_loaded:  { dot: "bg-gray-300",   text: "text-gray-500",   label: "not loaded" },
  unknown:     { dot: "bg-gray-300",   text: "text-gray-500",   label: "unknown" },
};

const trunks = ref<Trunk[]>([]);
const loading = ref(true);
const error = ref("");
const reloadingFS = ref(false);
const reloadMsg = ref("");

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
  active: true,
});

const providerTypes = ["generic", "exotel", "airtel", "jio", "tata"];

const formTitle = computed(() => (editing.value ? "Edit trunk" : "New trunk"));

async function load() {
  loading.value = true;
  error.value = "";
  try {
    trunks.value = await api.get<Trunk[]>("/v1/trunks");
  } catch (e) {
    error.value = e instanceof Error ? e.message : "failed to load";
  } finally {
    loading.value = false;
  }
}

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
    active: true,
  };
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
    active: full.active,
  };
  showForm.value = true;
}

async function save() {
  saving.value = true;
  error.value = "";
  try {
    const body = { ...form.value };
    if (editing.value) {
      await api.patch<Trunk>(`/v1/trunks/${editing.value.id}`, body);
    } else {
      await api.post<Trunk>("/v1/trunks", body);
    }
    showForm.value = false;
    editing.value = null;
    await load();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "save failed";
  } finally {
    saving.value = false;
  }
}

async function removeTrunk(t: Trunk) {
  if (!confirm(`Delete trunk "${t.name}"?`)) return;
  error.value = "";
  try {
    await api.del(`/v1/trunks/${t.id}`);
    await load();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "delete failed";
  }
}

async function reloadFS() {
  reloadingFS.value = true;
  reloadMsg.value = "";
  try {
    await api.post("/v1/trunks/reload");
    reloadMsg.value = "FS gateway dir + sofia rescan triggered.";
  } catch (e) {
    reloadMsg.value = e instanceof Error ? e.message : "reload failed";
  } finally {
    reloadingFS.value = false;
    setTimeout(() => (reloadMsg.value = ""), 4000);
  }
}

let refreshTimer: ReturnType<typeof setInterval> | null = null;

onMounted(() => {
  load();
  refreshTimer = setInterval(load, 10000);
});
onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer);
});
</script>

<template>
  <div class="space-y-5">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-semibold">Trunks</h1>
      <div class="flex items-center gap-2">
        <button
          class="text-sm text-gray-600 hover:text-gray-900 disabled:opacity-50"
          :disabled="reloadingFS"
          @click="reloadFS"
          title="Re-render gateway XML and rescan sofia"
        >
          {{ reloadingFS ? "Reloading…" : "Reload FS" }}
        </button>
        <button
          class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800"
          @click="newTrunk(); showForm = !showForm"
        >
          {{ showForm ? "Cancel" : "New trunk" }}
        </button>
      </div>
    </div>

    <p v-if="error" class="text-sm text-red-600">{{ error }}</p>
    <p v-if="reloadMsg" class="text-sm text-blue-600">{{ reloadMsg }}</p>

    <!-- Form -->
    <form
      v-if="showForm"
      class="bg-white rounded-xl border border-gray-200 p-5 space-y-4"
      @submit.prevent="save"
    >
      <div class="font-medium">{{ formTitle }}</div>

      <div class="grid sm:grid-cols-2 gap-4">
        <label class="block">
          <span class="text-sm font-medium text-gray-700">Name</span>
          <input
            v-model="form.name"
            required
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
            placeholder="Tata production"
          />
        </label>
        <label class="block">
          <span class="text-sm font-medium text-gray-700">Provider type</span>
          <select
            v-model="form.provider_type"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
          >
            <option v-for="p in providerTypes" :key="p" :value="p">{{ p }}</option>
          </select>
        </label>
      </div>

      <div class="flex items-center gap-6 pt-2 border-t border-gray-100">
        <label class="flex items-center gap-2">
          <input v-model="form.register" type="checkbox" class="rounded" />
          <span class="text-sm font-medium"
            >REGISTER auth (aggregator-style)</span
          >
        </label>
        <label class="flex items-center gap-2">
          <input v-model="form.active" type="checkbox" class="rounded" />
          <span class="text-sm">Active</span>
        </label>
      </div>

      <!-- REGISTER auth fields -->
      <template v-if="form.register">
        <div class="grid sm:grid-cols-2 gap-4">
          <label class="block">
            <span class="text-sm font-medium text-gray-700">SIP username</span>
            <input
              v-model="form.sip_username"
              required
              class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm font-mono"
            />
          </label>
          <label class="block">
            <span class="text-sm font-medium text-gray-700">SIP password</span>
            <input
              v-model="form.sip_password"
              type="password"
              required
              class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm font-mono"
            />
          </label>
        </div>
        <label class="block">
          <span class="text-sm font-medium text-gray-700">SIP realm</span>
          <input
            v-model="form.sip_realm"
            required
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm font-mono"
            placeholder="account.pstn.exotel.com"
          />
        </label>
      </template>

      <!-- IP-auth fields -->
      <template v-else>
        <div class="grid sm:grid-cols-2 gap-4">
          <label class="block">
            <span class="text-sm font-medium text-gray-700">SBC proxy IP</span>
            <input
              v-model="form.proxy_ip"
              required
              class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm font-mono"
              placeholder="10.0.0.42"
            />
            <span class="text-xs text-gray-400"
              >Tata's SBC IP — sent to and trusted from.</span
            >
          </label>
          <label class="block">
            <span class="text-sm font-medium text-gray-700">SBC ACL CIDR</span>
            <input
              v-model="form.auth_acl_cidr"
              class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm font-mono"
              placeholder="10.0.0.40/29"
            />
            <span class="text-xs text-gray-400">For the inbound ACL.</span>
          </label>
        </div>
        <label class="block">
          <span class="text-sm font-medium text-gray-700"
            >SIP realm (optional — defaults to proxy IP)</span
          >
          <input
            v-model="form.sip_realm"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm font-mono"
          />
        </label>
      </template>

      <div class="flex items-center gap-3 pt-2">
        <button
          type="submit"
          :disabled="saving"
          class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800 disabled:opacity-50"
        >
          {{ saving ? "Saving…" : editing ? "Save changes" : "Create" }}
        </button>
        <button
          type="button"
          class="text-sm text-gray-600 hover:text-gray-900"
          @click="showForm = false; editing = null"
        >
          Cancel
        </button>
      </div>
    </form>

    <!-- List -->
    <div class="bg-white rounded-xl border border-gray-200">
      <table class="w-full text-sm">
        <thead class="text-left text-gray-500">
          <tr>
            <th class="px-5 py-2 font-medium">Name</th>
            <th class="px-5 py-2 font-medium">Provider</th>
            <th class="px-5 py-2 font-medium">Status</th>
            <th class="px-5 py-2 font-medium">Auth</th>
            <th class="px-5 py-2 font-medium">Endpoint</th>
            <th class="px-5 py-2 font-medium">Active</th>
            <th class="px-5 py-2 font-medium"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="t in trunks" :key="t.id" class="border-t border-gray-200">
            <td class="px-5 py-2 font-medium">{{ t.name }}</td>
            <td class="px-5 py-2 font-mono text-xs">{{ t.provider_type }}</td>
            <td class="px-5 py-2">
              <span class="inline-flex items-center gap-1.5">
                <span
                  :class="['h-2 w-2 rounded-full', statusStyle[t.status || 'unknown'].dot]"
                  :title="t.state_raw || '(no FS state)'"
                />
                <span :class="['text-xs', statusStyle[t.status || 'unknown'].text]">
                  {{ statusStyle[t.status || 'unknown'].label }}
                </span>
              </span>
            </td>
            <td class="px-5 py-2 text-xs">
              <span v-if="t.register" class="text-purple-700">REGISTER</span>
              <span v-else class="text-amber-700">IP-auth</span>
            </td>
            <td class="px-5 py-2 font-mono text-xs">
              {{ t.register ? `${t.sip_username}@${t.sip_realm}` : t.proxy_ip || "—" }}
            </td>
            <td class="px-5 py-2">
              <span :class="t.active ? 'text-green-600' : 'text-gray-400'">
                {{ t.active ? "yes" : "no" }}
              </span>
            </td>
            <td class="px-5 py-2 text-right whitespace-nowrap">
              <button
                class="text-gray-600 hover:text-gray-900 mr-3"
                @click="startEdit(t)"
              >
                Edit
              </button>
              <button class="text-red-600 hover:text-red-800" @click="removeTrunk(t)">
                Delete
              </button>
            </td>
          </tr>
          <tr v-if="!loading && trunks.length === 0">
            <td colspan="7" class="px-5 py-8 text-center text-gray-400">
              No trunks yet — click <span class="font-medium">New trunk</span> to add one.
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <p class="text-xs text-gray-400">
      Saving a trunk renders gateway XML to
      <code class="font-mono">freeswitch/conf/sip_profiles/external/dynamic/</code>
      and runs
      <code class="font-mono">sofia profile external rescan</code>. Zero downtime;
      live calls stay up.
    </p>
  </div>
</template>
