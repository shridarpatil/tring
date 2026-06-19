<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from "vue";
import { Play, Loader2, Sparkles } from "lucide-vue-next";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import type { Audio } from "../api/types";

// Reusable sound selector: v-models a path string (an asset's file_path, "" for
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
  // A path that isn't a known asset and isn't a generated TTS prompt is a
  // hand-entered custom path → open the advanced input so it's visible/editable.
  if (
    props.modelValue &&
    !isGenerated.value &&
    !sounds.value.some((s) => s.file_path === props.modelValue)
  ) {
    custom.value = true;
  }
});

// A generated prompt lives under the /tts/ subdir (auto-rendered from node text
// on save). We show it as a friendly chip + play, not a raw hash path. `override`
// lets the operator swap in an uploaded file instead.
const isGenerated = computed(() => val.value.includes("/tts/"));
const override = ref(false);

const val = computed({
  get: () => props.modelValue,
  set: (v: string) => emit("update:modelValue", v),
});

// A path is previewable if it's a real file (not empty, not a stream URL like
// local_stream://moh). Covers both uploaded sounds and TTS-generated prompts.
const previewable = computed(() => !!val.value && !val.value.includes("://"));

// Play the selected sound. The audio endpoints need the console JWT, which a
// plain <audio src> can't send, so fetch the bytes as a blob (mirrors the call
// recording player) and play that.
const playing = ref(false);
let audioEl: HTMLAudioElement | null = null;
let blobUrl = "";

function stopPlayback() {
  if (audioEl) {
    audioEl.pause();
    audioEl = null;
  }
  if (blobUrl) {
    URL.revokeObjectURL(blobUrl);
    blobUrl = "";
  }
  playing.value = false;
}

async function play() {
  if (!previewable.value || playing.value) return;
  error.value = "";
  playing.value = true;
  try {
    const token = auth.token();
    const res = await fetch(`/v1/audio/preview?path=${encodeURIComponent(val.value)}`, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!res.ok) throw new Error();
    blobUrl = URL.createObjectURL(await res.blob());
    audioEl = new Audio(blobUrl);
    audioEl.onended = stopPlayback;
    audioEl.onerror = () => {
      error.value = "couldn't play this sound";
      stopPlayback();
    };
    await audioEl.play();
  } catch {
    error.value = "couldn't play this sound";
    stopPlayback();
  }
}

onBeforeUnmount(stopPlayback);

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
    val.value = a.file_path; // select the freshly uploaded sound
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
    <span v-if="label" class="text-xs font-medium text-ink-muted">{{ label }}</span>

    <!-- Generated-from-text prompt: a friendly chip + play, not the raw hash path. -->
    <template v-if="isGenerated && !override">
      <div class="mt-1 flex items-center gap-2">
        <div
          class="flex-1 inline-flex items-center gap-2 rounded-md bg-accent-soft/40 border border-accent/30 px-3 h-9 text-sm text-ink-muted"
        >
          <Sparkles class="h-3.5 w-3.5 text-accent shrink-0" />
          Audio generated from text
        </div>
        <button
          type="button"
          :title="playing ? 'Playing…' : 'Play'"
          aria-label="Play sound"
          class="shrink-0 inline-flex items-center justify-center rounded-md bg-surface-2 border border-border w-9 h-9 text-ink transition-colors hover:bg-surface hover:text-accent focus:outline-none focus:ring-2 focus:ring-accent/40 disabled:opacity-50"
          :disabled="playing"
          @click="play"
        >
          <component :is="playing ? Loader2 : Play" class="h-4 w-4" :class="playing ? 'animate-spin' : ''" />
        </button>
      </div>
      <button type="button" class="mt-1 text-[11px] text-ink-faint hover:text-ink-muted" @click="override = true">
        Use an uploaded file instead
      </button>
    </template>

    <template v-else-if="!custom">
      <div class="mt-1 flex gap-2">
        <select
          v-model="val"
          class="w-full rounded-md bg-surface-2 border border-border px-3 h-9 text-sm text-ink transition-colors focus:outline-none focus:ring-2 focus:ring-accent/40 focus:border-accent"
        >
          <option value="">{{ defaultLabel || "Default" }}</option>
          <option v-for="s in sounds" :key="s.id" :value="s.file_path">{{ s.name }} ({{ s.format }})</option>
        </select>
        <button
          v-if="previewable"
          type="button"
          :title="playing ? 'Playing…' : 'Play'"
          aria-label="Play sound"
          class="shrink-0 inline-flex items-center justify-center rounded-md bg-surface-2 border border-border w-9 h-9 text-ink transition-colors hover:bg-surface hover:text-accent focus:outline-none focus:ring-2 focus:ring-accent/40 disabled:opacity-50"
          :disabled="playing"
          @click="play"
        >
          <component :is="playing ? Loader2 : Play" class="h-4 w-4" :class="playing ? 'animate-spin' : ''" />
        </button>
        <button
          type="button"
          class="shrink-0 rounded-md bg-surface-2 border border-border px-3 h-9 text-sm text-ink transition-colors hover:bg-surface focus:outline-none focus:ring-2 focus:ring-accent/40 disabled:opacity-50"
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
      <button type="button" class="mt-1 text-[11px] text-ink-faint hover:text-ink-muted" @click="custom = true">
        Use a custom path
      </button>
    </template>
    <template v-else>
      <div class="mt-1 flex gap-2">
        <input
          v-model="val"
          class="w-full rounded-md bg-surface-2 border border-border px-3 h-9 text-sm font-mono text-ink placeholder:text-ink-faint transition-colors focus:outline-none focus:ring-2 focus:ring-accent/40 focus:border-accent"
          placeholder="local_stream://moh or /usr/share/freeswitch/sounds/..."
        />
        <button
          v-if="previewable"
          type="button"
          :title="playing ? 'Playing…' : 'Play'"
          aria-label="Play sound"
          class="shrink-0 inline-flex items-center justify-center rounded-md bg-surface-2 border border-border w-9 h-9 text-ink transition-colors hover:bg-surface hover:text-accent focus:outline-none focus:ring-2 focus:ring-accent/40 disabled:opacity-50"
          :disabled="playing"
          @click="play"
        >
          <component :is="playing ? Loader2 : Play" class="h-4 w-4" :class="playing ? 'animate-spin' : ''" />
        </button>
      </div>
      <button type="button" class="mt-1 text-[11px] text-ink-faint hover:text-ink-muted" @click="custom = false">
        Pick from the sound library
      </button>
    </template>
    <p v-if="error" class="text-[11px] text-danger mt-1">{{ error }}</p>
    <p v-else-if="!sounds.length && !custom" class="text-[11px] text-ink-faint mt-1">
      No sounds yet — click <strong>Upload</strong> to add one.
    </p>
  </div>
</template>
