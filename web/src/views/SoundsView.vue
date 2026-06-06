<script setup lang="ts">
import { ref } from "vue";
import { errMessage } from "../lib/errors";
import { useList } from "../composables/useList";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import type { Audio } from "../api/types";

const { items: sounds, loading, error, load } = useList<Audio>("/v1/audio");
const uploading = ref(false);
const name = ref("");
const fileInput = ref<HTMLInputElement | null>(null);
const previewUrl = ref<Record<string, string>>({});

async function upload() {
  const f = fileInput.value?.files?.[0];
  if (!f) {
    error.value = "choose a .wav or .mp3 file";
    return;
  }
  uploading.value = true;
  error.value = "";
  try {
    await api.upload<Audio>("/v1/audio", f, name.value ? { name: name.value } : undefined);
    name.value = "";
    if (fileInput.value) fileInput.value.value = "";
    await load();
  } catch (e) {
    error.value = errMessage(e, "upload failed");
  } finally {
    uploading.value = false;
  }
}

async function destroy(s: Audio) {
  if (!confirm(`Delete "${s.name}"? Flows or hold music using it will fall back to the default.`)) return;
  error.value = "";
  try {
    await api.del<void>(`/v1/audio/${s.id}`);
    await load();
  } catch (e) {
    error.value = errMessage(e, "delete failed");
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
    error.value = "preview failed";
  }
}

function fmtSize(n: number) {
  return n >= 1024 * 1024 ? (n / 1024 / 1024).toFixed(1) + " MB" : Math.max(1, Math.round(n / 1024)) + " KB";
}
</script>

<template>
  <div class="space-y-5">
    <h1 class="text-2xl font-semibold">Sounds</h1>
    <p class="text-sm text-gray-500">
      Upload audio (.wav or .mp3) once, then reuse it for IVR greetings and menu prompts, or as your
      queue hold music. Pick an uploaded sound on the <strong>Flows</strong> editor and in
      <strong>Settings</strong>.
    </p>
    <p v-if="error" class="text-sm text-red-600">{{ error }}</p>

    <form class="bg-white rounded-xl border p-5 grid sm:grid-cols-3 gap-4 items-end" @submit.prevent="upload">
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Name (optional)</span>
        <input
          v-model="name"
          class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
          placeholder="Welcome greeting"
        />
      </label>
      <label class="block">
        <span class="text-sm font-medium text-gray-700">File (.wav / .mp3)</span>
        <input
          ref="fileInput"
          type="file"
          accept=".wav,.mp3,audio/wav,audio/mpeg"
          class="mt-1 w-full text-sm"
        />
      </label>
      <button
        type="submit"
        :disabled="uploading"
        class="bg-gray-900 text-white text-sm rounded-md px-4 py-2 hover:bg-gray-800 disabled:opacity-50"
      >
        {{ uploading ? "Uploading…" : "Upload" }}
      </button>
    </form>

    <div class="bg-white rounded-xl border">
      <table class="w-full text-sm">
        <thead class="text-left text-gray-500">
          <tr>
            <th class="px-5 py-2 font-medium">Name</th>
            <th class="px-5 py-2 font-medium">Format</th>
            <th class="px-5 py-2 font-medium">Size</th>
            <th class="px-5 py-2 font-medium">Preview</th>
            <th class="px-5 py-2 font-medium"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in sounds" :key="s.id" class="border-t">
            <td class="px-5 py-2 font-medium">{{ s.name }}</td>
            <td class="px-5 py-2 uppercase text-gray-500">{{ s.format }}</td>
            <td class="px-5 py-2 text-gray-500">{{ fmtSize(s.size_bytes) }}</td>
            <td class="px-5 py-2">
              <audio v-if="previewUrl[s.id]" :src="previewUrl[s.id]" controls class="h-8"></audio>
              <button v-else class="text-gray-600 hover:text-gray-900" @click="preview(s)">▶ Play</button>
            </td>
            <td class="px-5 py-2 text-right">
              <button class="text-red-600 hover:text-red-800" @click="destroy(s)">Delete</button>
            </td>
          </tr>
          <tr v-if="!loading && sounds.length === 0">
            <td colspan="5" class="px-5 py-6 text-center text-gray-400">No sounds yet</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
