<script setup lang="ts">
import { ref } from "vue";
import { errMessage } from "../lib/errors";
import { useList } from "../composables/useList";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import { toast } from "../lib/toast";
import type { Audio } from "../api/types";
import { Button, IconButton, Panel, DataTable, type Column } from "../components/ui";
import { Trash2 } from "lucide-vue-next";

const { items: sounds, loading, load } = useList<Audio>("/v1/audio");
const uploading = ref(false);
const name = ref("");
const fileInput = ref<HTMLInputElement | null>(null);
const previewUrl = ref<Record<string, string>>({});

const cols: Column[] = [
  { key: "name", label: "Name" },
  { key: "format", label: "Format" },
  { key: "size_bytes", label: "Size" },
  { key: "preview", label: "Preview" },
];

async function upload() {
  const f = fileInput.value?.files?.[0];
  if (!f) {
    toast.error("choose a .wav or .mp3 file");
    return;
  }
  uploading.value = true;
  try {
    await api.upload<Audio>("/v1/audio", f, name.value ? { name: name.value } : undefined);
    name.value = "";
    if (fileInput.value) fileInput.value.value = "";
    await load();
    toast.success("Sound uploaded");
  } catch (e) {
    toast.error(errMessage(e, "upload failed"));
  } finally {
    uploading.value = false;
  }
}

async function destroy(s: Audio) {
  if (!confirm(`Delete "${s.name}"? Flows or hold music using it will fall back to the default.`)) return;
  try {
    await api.del<void>(`/v1/audio/${s.id}`);
    await load();
    toast.success("Sound deleted");
  } catch (e) {
    toast.error(errMessage(e, "delete failed"));
  }
}

// <audio src> can't send the auth header, so fetch the file as a blob (with the
// console JWT) and play from an object URL.
async function preview(s: Audio) {
  if (previewUrl.value[s.id]) return;
  const token = auth.token();
  const res = await fetch(`/v1/audio/${s.id}/file`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  });
  if (res.ok) {
    previewUrl.value = { ...previewUrl.value, [s.id]: URL.createObjectURL(await res.blob()) };
  } else {
    toast.error("preview failed");
  }
}

function fmtSize(n: number) {
  return n >= 1024 * 1024 ? (n / 1024 / 1024).toFixed(1) + " MB" : Math.max(1, Math.round(n / 1024)) + " KB";
}
</script>

<template>
  <div class="view">
    <h1 class="page-title">Sounds</h1>
    <p class="text-light">
      Upload audio (.wav or .mp3) once, then reuse it for IVR greetings and menu prompts, or as your
      queue hold music. Pick an uploaded sound on the <strong>Flows</strong> editor and in
      <strong>Settings</strong>.
    </p>

    <form class="card grid grid-3" style="align-items: end" @submit.prevent="upload">
      <div data-field>
        <label for="snd-name">Name (optional)</label>
        <input id="snd-name" v-model="name" placeholder="Welcome greeting" />
      </div>
      <div data-field>
        <label for="snd-file">File (.wav / .mp3)</label>
        <input id="snd-file" ref="fileInput" type="file" accept=".wav,.mp3,audio/wav,audio/mpeg" />
      </div>
      <Button type="submit" :loading="uploading">Upload</Button>
    </form>

    <Panel>
      <DataTable :columns="cols" :rows="sounds" :loading="loading" empty-text="No sounds yet">
        <template #cell-format="{ value }">
          <span class="text-light" style="text-transform: uppercase">{{ value }}</span>
        </template>
        <template #cell-size_bytes="{ value }">
          <span class="text-light">{{ fmtSize(Number(value)) }}</span>
        </template>
        <template #cell-preview="{ row }">
          <audio v-if="previewUrl[row.id]" :src="previewUrl[row.id]" controls class="h-8"></audio>
          <Button v-else variant="ghost" size="sm" @click="preview(row)">▶ Play</Button>
        </template>
        <template #row-actions="{ row }">
          <IconButton title="Delete" variant="danger" @click="destroy(row)">
            <Trash2 />
          </IconButton>
        </template>
      </DataTable>
    </Panel>
  </div>
</template>
