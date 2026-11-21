<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { errMessage } from "../lib/errors";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import { confirmDialog } from "../lib/confirm";
import type { RecordingStorage } from "../api/types";
import { Button, Badge, TextField, Toggle } from "./ui";

// Settings → Recording → Storage. Saved on its own (not the page's save bar):
// the server test-writes the bucket before saving, and the secret is
// write-only — blank keeps the stored one.
const PATH = "/v1/tenants/me/recording-storage";

const current = ref<RecordingStorage | null>(null);
const form = ref({ bucket: "", region: "", endpoint: "", prefix: "recordings", use_ssl: true, access_key: "", secret_key: "" });
const saving = ref(false);
const error = ref("");

function fill(s: RecordingStorage) {
  current.value = s;
  form.value = { bucket: s.bucket, region: s.region, endpoint: s.endpoint, prefix: s.prefix, use_ssl: s.use_ssl, access_key: s.access_key, secret_key: "" };
}

onMounted(async () => {
  try {
    fill(await api.get<RecordingStorage>(PATH));
  } catch (e) {
    toast.error(errMessage(e, "failed to load recording storage"));
  }
});

const mode = computed(() => {
  const s = current.value;
  if (!s || !s.bucket) return { tone: "neutral" as const, text: "Local disk" };
  return s.access_key ? { tone: "success" as const, text: "S3 · access keys" } : { tone: "success" as const, text: "S3 · IAM role" };
});
const hasKeys = computed(() => !!current.value && (current.value.has_secret || !!current.value.access_key));

async function save() {
  saving.value = true;
  error.value = "";
  try {
    const { secret_key, ...rest } = form.value;
    const body = secret_key ? { ...rest, secret_key } : rest;
    fill(await api.put<RecordingStorage>(PATH, body));
    toast.success(form.value.bucket ? "Storage saved — test upload succeeded" : "Storage saved — recordings stay on local disk");
  } catch (e) {
    // Kept next to the fields: a failed test write needs the S3 error in view.
    error.value = errMessage(e, "save failed");
  } finally {
    saving.value = false;
  }
}

async function removeKeys() {
  if (!(await confirmDialog("Remove the access key and secret? Uploads will use the server's IAM role."))) return;
  try {
    await api.del(`${PATH}/keys`);
    fill(await api.get<RecordingStorage>(PATH));
    toast.success("Keys removed — using the IAM role");
  } catch (e) {
    toast.error(errMessage(e, "remove failed"));
  }
}
</script>

<template>
  <div class="srow stacked">
    <div class="hstack justify-between">
      <div>
        <div class="srow-label">Storage</div>
        <p class="srow-desc">
          Where finished recordings are uploaded. Leave the bucket empty to keep them on this server's disk. Leave
          the keys empty to use the server's IAM role.
        </p>
      </div>
      <Badge :tone="mode.tone">{{ mode.text }}</Badge>
    </div>
    <form class="vstack" @submit.prevent="save">
      <div class="grid grid-2">
        <TextField v-model="form.bucket" label="Bucket" mono placeholder="acme-recordings" hint="Empty = local disk." />
        <TextField v-model="form.region" label="Region" mono placeholder="ap-south-1" />
      </div>
      <div class="grid grid-2">
        <TextField v-model="form.endpoint" label="Endpoint (optional)" mono placeholder="minio.internal:9000" hint="Empty = AWS S3. Set for MinIO or R2." />
        <TextField v-model="form.prefix" label="Prefix" mono placeholder="recordings" />
      </div>
      <div class="grid grid-2">
        <TextField v-model="form.access_key" label="Access key (optional)" mono autocomplete="off" hint="Empty = server's IAM role." />
        <TextField
          v-model="form.secret_key"
          type="password"
          label="Secret key (optional)"
          mono
          autocomplete="new-password"
          :placeholder="current?.has_secret ? '••• set (unchanged)' : ''"
          :hint="current?.has_secret ? 'Leave blank to keep the current secret.' : undefined"
        />
      </div>
      <Toggle v-model="form.use_ssl" label="Use HTTPS" />
      <p v-if="error" role="alert" data-variant="error">{{ error }}</p>
      <div class="hstack justify-between">
        <span class="text-lighter">Saving uploads and deletes a small test file. Applies to the next recording.</span>
        <div class="hstack">
          <Button v-if="hasKeys" type="button" variant="ghost" @click="removeKeys">Remove keys</Button>
          <Button type="submit" :loading="saving">Save storage</Button>
        </div>
      </div>
    </form>
  </div>
</template>
