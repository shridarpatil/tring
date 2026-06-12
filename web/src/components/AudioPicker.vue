<script setup lang="ts">
import { ref, onMounted, computed } from "vue";
import { api } from "../api/client";
import type { Audio } from "../api/types";

// Reusable sound selector: v-models a path string (an asset's fs_path, "" for
// default, or a raw path via the advanced escape hatch). Pick an existing sound,
// OR upload a new one inline (uploads transcode to Opus-Ogg server-side so they
// play on both the web and phone planes).
const props = defineProps<{
  modelValue: string;
  label?: string;
  defaultLabel?: string; // text for the empty option (e.g. "Use text-to-speech" / "Default")
}>();
const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();

const sounds = ref<Audio[]>([]);
const custom = ref(false);
const uploading = ref(false);
const error = ref("");
const fileInput = ref<HTMLInputElement | null>(null);

onMounted(async () => {
  try {
    sounds.value = await api.get<Audio[]>("/v1/audio");
  } catch {
    /* leave empty — the custom-path escape hatch still works */
  }
  if (props.modelValue && !sounds.value.some((s) => s.fs_path === props.modelValue)) {
    custom.value = true;
  }
});

const val = computed({
  get: () => props.modelValue,
  set: (v: string) => emit("update:modelValue", v),
});

async function onFile(ev: Event) {
  const input = ev.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) return;
  uploading.value = true;
  error.value = "";
  try {
    const a = await api.upload<Audio>("/v1/audio", file, {
      name: file.name.replace(/\.[^.]+$/, ""),
    });
    sounds.value.push(a);
    val.value = a.fs_path; // select the freshly uploaded sound
  } catch (e) {
    error.value = (e as Error)?.message || "upload failed";
  } finally {
    uploading.value = false;
    if (fileInput.value) fileInput.value.value = "";
  }
}
</script>

<template>
  <div>
    <span v-if="label" class="text-xs font-medium text-gray-700">{{ label }}</span>
    <template v-if="!custom">
      <div class="mt-1 flex gap-2">
        <select v-model="val" class="w-full rounded-md border border-gray-300 px-3 py-2 text-sm">
          <option value="">{{ defaultLabel || "Default" }}</option>
          <option v-for="s in sounds" :key="s.id" :value="s.fs_path">{{ s.name }} ({{ s.format }})</option>
        </select>
        <button
          type="button"
          class="shrink-0 rounded-md border border-gray-300 px-3 py-2 text-sm hover:bg-gray-50 disabled:opacity-50"
          :disabled="uploading"
          @click="fileInput?.click()"
        >
          {{ uploading ? "Uploading…" : "Upload" }}
        </button>
        <input
          ref="fileInput"
          type="file"
          accept=".wav,.mp3,.ogg,.m4a"
          class="hidden"
          @change="onFile"
        />
      </div>
      <button type="button" class="mt-1 text-[11px] text-gray-400 hover:text-gray-600" @click="custom = true">
        Use a custom path
      </button>
    </template>
    <template v-else>
      <input
        v-model="val"
        class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm font-mono"
        placeholder="local_stream://moh or /usr/share/freeswitch/sounds/..."
      />
      <button type="button" class="mt-1 text-[11px] text-gray-400 hover:text-gray-600" @click="custom = false">
        Pick from the sound library
      </button>
    </template>
    <p v-if="error" class="text-[11px] text-red-600 mt-1">{{ error }}</p>
    <p v-else-if="!sounds.length && !custom" class="text-[11px] text-gray-400 mt-1">
      No sounds yet — click <strong>Upload</strong> to add one.
    </p>
  </div>
</template>
