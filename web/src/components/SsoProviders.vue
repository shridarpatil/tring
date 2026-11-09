<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { errMessage } from "../lib/errors";
import { useList } from "../composables/useList";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import { confirmDialog } from "../lib/confirm";
import { deleteProviderMessage, splitDomains } from "../lib/sso";
import type { SsoProvider } from "../api/types";
import { Button, DataTable, type Column, Badge, Modal, TextField, Toggle, CopyButton } from "./ui";

// The body of Settings → Single sign-on (the card header lives in SettingsView).
const { items: providers, loading, load } = useList<SsoProvider>("/v1/sso-providers");

const callbackUrl = ref("");
onMounted(async () => {
  try {
    callbackUrl.value = (await api.get<{ callback_url: string }>("/v1/sso-providers/callback-url")).callback_url;
  } catch (e) {
    toast.error(errMessage(e, "failed to load callback URL"));
  }
});

const cols: Column[] = [
  { key: "name", label: "Name" },
  { key: "domains", label: "Domains" },
  { key: "enabled", label: "Status" },
];

const showForm = ref(false);
const editing = ref<SsoProvider | null>(null);
const saving = ref(false);
const formError = ref("");
const form = ref({ name: "", issuer_url: "", client_id: "", client_secret: "", domains: "", require_sso: false, enabled: true });
const title = computed(() => (editing.value ? "Edit identity provider" : "Add identity provider"));

function openAdd() {
  editing.value = null;
  formError.value = "";
  form.value = { name: "", issuer_url: "", client_id: "", client_secret: "", domains: "", require_sso: false, enabled: true };
  showForm.value = true;
}

function openEdit(p: SsoProvider) {
  editing.value = p;
  formError.value = "";
  form.value = {
    name: p.name,
    issuer_url: p.issuer_url,
    client_id: p.client_id,
    client_secret: "",
    domains: p.domains.join(", "),
    require_sso: p.require_sso,
    enabled: p.enabled,
  };
  showForm.value = true;
}

async function save() {
  saving.value = true;
  formError.value = "";
  try {
    const f = form.value;
    const body: Record<string, unknown> = {
      name: f.name.trim(),
      issuer_url: f.issuer_url.trim(),
      client_id: f.client_id.trim(),
      domains: splitDomains(f.domains),
      require_sso: f.require_sso,
      enabled: f.enabled,
    };
    // Blank on edit keeps the stored secret.
    if (f.client_secret || !editing.value) body.client_secret = f.client_secret;
    if (editing.value) {
      await api.patch<SsoProvider>(`/v1/sso-providers/${editing.value.id}`, body);
      toast.success("Provider saved");
    } else {
      await api.post<SsoProvider>("/v1/sso-providers", body);
      toast.success("Provider added");
    }
    showForm.value = false;
    await load();
  } catch (e) {
    // Kept inside the dialog: discovery failures need the message next to the fields.
    formError.value = errMessage(e, "save failed");
  } finally {
    saving.value = false;
  }
}

async function remove(p: SsoProvider) {
  if (!(await confirmDialog(deleteProviderMessage(p.name, p.sso_only_users)))) return;
  try {
    await api.del(`/v1/sso-providers/${p.id}`);
    await load();
    toast.success("Provider deleted");
  } catch (e) {
    toast.error(errMessage(e, "delete failed"));
  }
}
</script>

<template>
  <div class="vstack" style="padding: 1.25rem 1.5rem">
    <div class="hstack justify-between">
      <div class="vstack gap-2">
        <span style="font-size: var(--text-7); font-weight: 500">Redirect URI</span>
        <div class="hstack">
          <code class="mono">{{ callbackUrl || "…" }}</code>
          <CopyButton v-if="callbackUrl" :value="callbackUrl" />
        </div>
        <span class="text-lighter">Register this as the redirect URI in your identity provider.</span>
      </div>
      <Button @click="openAdd">Add provider</Button>
    </div>

    <DataTable :columns="cols" :rows="providers" :loading="loading" empty-text="No identity provider configured">
      <template #cell-domains="{ row }">
        <div class="chip-wrap">
          <Badge v-for="d in row.domains" :key="d" tone="neutral">{{ d }}</Badge>
        </div>
      </template>
      <template #cell-enabled="{ row }">
        <div class="chip-wrap">
          <Badge :tone="row.enabled ? 'success' : 'neutral'">{{ row.enabled ? "Enabled" : "Disabled" }}</Badge>
          <Badge v-if="row.require_sso" tone="warning">Require SSO</Badge>
        </div>
      </template>
      <template #row-actions="{ row }">
        <div class="hstack">
          <Button variant="secondary" size="sm" @click="openEdit(row)">Edit</Button>
          <Button variant="danger" size="sm" @click="remove(row)">Delete</Button>
        </div>
      </template>
    </DataTable>

    <Modal v-model:open="showForm" :title="title">
      <form id="sso-form" class="vstack" @submit.prevent="save">
        <TextField v-model="form.name" label="Name" required placeholder="Okta" />
        <TextField v-model="form.issuer_url" label="Issuer URL" type="url" required placeholder="https://acme.okta.com" />
        <TextField v-model="form.client_id" label="Client ID" required autocomplete="off" />
        <TextField
          v-model="form.client_secret"
          type="password"
          label="Client secret"
          :required="!editing"
          autocomplete="off"
          :placeholder="editing ? 'unchanged' : ''"
        />
        <TextField v-model="form.domains" label="Email domains" required placeholder="acme.com, acme.io" />
        <Toggle v-model="form.enabled" label="Enabled" />
        <Toggle v-model="form.require_sso" label="Require SSO (admins can still use a password)" />
        <p v-if="formError" role="alert" data-variant="error">{{ formError }}</p>
      </form>
      <template #footer>
        <Button variant="ghost" @click="showForm = false">Cancel</Button>
        <Button type="submit" form="sso-form" :loading="saving">{{ editing ? "Save" : "Add" }}</Button>
      </template>
    </Modal>
  </div>
</template>
