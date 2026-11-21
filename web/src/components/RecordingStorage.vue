<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { errMessage } from "../lib/errors";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import { confirmDialog } from "../lib/confirm";
import type { RecordingStorage } from "../api/types";
import { Button, Badge, Select, TextField, Toggle } from "./ui";

// The form under Settings → Recording → Storage (the row heading lives in
// SettingsView, whose row styles are scoped). Saved on its own, not with the
// page's save bar: the server test-writes the bucket before saving, and the
// secret is write-only — blank keeps the stored one.
const PATH = "/v1/tenants/me/recording-storage";

const kinds = [
  { value: "local", label: "Local disk (this server)" },
  { value: "s3", label: "Amazon S3 or S3-compatible" },
];

const current = ref<RecordingStorage | null>(null);
const kind = ref("local");
const form = ref({
  bucket: "",
  region: "",
  endpoint: "",
  prefix: "recordings",
  use_ssl: true,
  access_key: "",
  secret_key: "",
});
const saving = ref(false);
const error = ref("");
// A save that worked but needs attention, e.g. the test file couldn't be
// deleted because the role lacks s3:DeleteObject.
const warning = ref("");

function fill(s: RecordingStorage) {
  current.value = s;
  kind.value = s.bucket ? "s3" : "local";
  form.value = {
    bucket: s.bucket,
    region: s.region,
    endpoint: s.endpoint,
    prefix: s.prefix,
    use_ssl: s.use_ssl,
    access_key: s.access_key,
    secret_key: "",
  };
}

onMounted(async () => {
  try {
    fill(await api.get<RecordingStorage>(PATH));
  } catch (e) {
    toast.error(errMessage(e, "failed to load recording storage"));
  }
});

// What is saved now, independent of unsaved edits.
const mode = computed(() => {
  const s = current.value;
  if (!s || !s.bucket)
    return { tone: "neutral" as const, text: "Now: local disk" };
  return {
    tone: "success" as const,
    text: s.access_key ? "Now: S3 · access keys" : "Now: S3 · IAM role",
  };
});
const hasKeys = computed(
  () =>
    !!current.value && (current.value.has_secret || !!current.value.access_key),
);

async function save() {
  saving.value = true;
  error.value = "";
  warning.value = "";
  try {
    const { secret_key, ...rest } = form.value;
    // Local disk = no bucket; the other S3 fields are kept for switching back.
    const fields = {
      ...rest,
      bucket: kind.value === "s3" ? rest.bucket.trim() : "",
    };
    const body = secret_key ? { ...fields, secret_key } : fields;
    const saved = await api.put<RecordingStorage>(PATH, body);
    fill(saved);
    if (saved.warning) {
      warning.value = saved.warning;
      toast.info("Storage saved, with a warning — see below");
      return;
    }
    toast.success(
      kind.value === "s3"
        ? "Storage saved — test upload succeeded"
        : "Storage saved — recordings stay on this server",
    );
  } catch (e) {
    // Kept next to the fields: a failed test write needs the S3 error in view.
    error.value = errMessage(e, "save failed");
  } finally {
    saving.value = false;
  }
}

async function removeKeys() {
  if (
    !(await confirmDialog(
      "Remove the access key and secret? Uploads will use the server's IAM role.",
    ))
  )
    return;
  try {
    await api.del(`${PATH}/keys`);
    // Only the keys change: keep any unsaved edits to the other fields.
    if (current.value)
      current.value = { ...current.value, access_key: "", has_secret: false };
    form.value = { ...form.value, access_key: "", secret_key: "" };
    toast.success("Keys removed — using the IAM role");
  } catch (e) {
    toast.error(errMessage(e, "remove failed"));
  }
}
</script>

<template>
  <form class="vstack" @submit.prevent="save">
    <div class="grid grid-2">
      <Select v-model="kind" label="Storage type" :options="kinds" />
      <div class="hstack">
        <Badge :tone="mode.tone">{{ mode.text }}</Badge>
      </div>
    </div>

    <template v-if="kind === 's3'">
      <div class="grid grid-2">
        <TextField
          v-model="form.bucket"
          label="Bucket"
          mono
          required
          placeholder="acme-recordings"
        />
        <TextField
          v-model="form.region"
          label="Region"
          mono
          :required="!form.endpoint"
          placeholder="ap-south-1"
          :hint="
            form.endpoint ? 'Optional for MinIO/R2.' : 'Required for AWS S3.'
          "
        />
      </div>
      <div class="grid grid-2">
        <TextField
          v-model="form.endpoint"
          label="Endpoint (optional)"
          mono
          placeholder="minio.internal:9000"
          hint="Empty = AWS S3. Set for MinIO or R2."
        />
        <TextField
          v-model="form.prefix"
          label="Prefix"
          mono
          placeholder="recordings"
        />
      </div>
      <div class="grid grid-2">
        <TextField
          v-model="form.access_key"
          label="Access key (optional)"
          mono
          autocomplete="off"
          hint="Empty = server's IAM role."
        />
        <TextField
          v-model="form.secret_key"
          type="password"
          label="Secret key (optional)"
          mono
          autocomplete="new-password"
          :placeholder="current?.has_secret ? '••• set (unchanged)' : ''"
          :hint="
            current?.has_secret
              ? 'Leave blank to keep the current secret.'
              : undefined
          "
        />
      </div>
      <Toggle v-model="form.use_ssl" label="Use HTTPS" />
    </template>
    <p v-else class="text-lighter">
      Recordings are kept in this server's recordings folder and served from
      there.
    </p>

    <p v-if="error" role="alert" data-variant="error">{{ error }}</p>
    <p v-if="warning" role="alert" data-variant="warning">{{ warning }}</p>
    <div class="hstack justify-between">
      <span class="text-lighter">
        {{
          kind === "s3"
            ? "Saving uploads and deletes a small test file. Applies to the next recording."
            : "Applies to the next recording."
        }}
      </span>
      <div class="hstack">
        <Button
          v-if="kind === 's3' && hasKeys"
          type="button"
          variant="ghost"
          @click="removeKeys"
          >Remove keys</Button
        >
        <Button type="submit" :loading="saving">Save storage</Button>
      </div>
    </div>
  </form>
</template>
