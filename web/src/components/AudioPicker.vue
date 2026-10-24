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
  modelValue?: string; // unset (a node with no audio_file yet) = default
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
  // hand-entered custom path -> open the advanced input so it's visible/editable.
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
  get: () => props.modelValue ?? "",
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
    <span v-if="label" class="ap-label">{{ label }}</span>

    <!-- Generated-from-text prompt: a friendly chip + play, not the raw hash path. -->
    <template v-if="isGenerated && !override">
      <div class="ap-row">
        <div class="gen-chip">
          <Sparkles :size="14" class="gen-spark" />
          Audio generated from text
        </div>
        <button
          type="button"
          class="icon secondary"
          :title="playing ? 'Playing…' : 'Play'"
          aria-label="Play sound"
          :disabled="playing"
          @click="play"
        >
          <component :is="playing ? Loader2 : Play" :size="16" :class="playing ? 'spin' : ''" />
        </button>
      </div>
      <button type="button" class="plain link" @click="override = true">
        Use an uploaded file instead
      </button>
    </template>

    <template v-else-if="!custom">
      <div class="ap-row">
        <select v-model="val">
          <option value="">{{ defaultLabel || "Default" }}</option>
          <option v-for="s in sounds" :key="s.id" :value="s.file_path">
            {{ s.name }} ({{ s.format }})
          </option>
        </select>
        <button
          v-if="previewable"
          type="button"
          class="icon secondary"
          :title="playing ? 'Playing…' : 'Play'"
          aria-label="Play sound"
          :disabled="playing"
          @click="play"
        >
          <component :is="playing ? Loader2 : Play" :size="16" :class="playing ? 'spin' : ''" />
        </button>
        <button type="button" class="secondary" :disabled="uploading" @click="fileInput?.click()">
          {{ uploading ? "Uploading…" : "Upload" }}
        </button>
        <input
          ref="fileInput"
          type="file"
          accept=".wav,.mp3,.ogg,.m4a"
          class="ap-hidden"
          @change="onFile"
        />
      </div>
      <button type="button" class="plain link" @click="custom = true">Use a custom path</button>
    </template>

    <template v-else>
      <div class="ap-row">
        <input
          v-model="val"
          class="mono"
          placeholder="local_stream://moh or /usr/share/freeswitch/sounds/..."
        />
        <button
          v-if="previewable"
          type="button"
          class="icon secondary"
          :title="playing ? 'Playing…' : 'Play'"
          aria-label="Play sound"
          :disabled="playing"
          @click="play"
        >
          <component :is="playing ? Loader2 : Play" :size="16" :class="playing ? 'spin' : ''" />
        </button>
      </div>
      <button type="button" class="plain link" @click="custom = false">
        Pick from the sound library
      </button>
    </template>

    <p v-if="error" class="ap-err">{{ error }}</p>
    <p v-else-if="!sounds.length && !custom" class="ap-hint">
      No sounds yet — click <strong>Upload</strong> to add one.
    </p>
  </div>
</template>

<style scoped>
.ap-label {
  display: block;
  font-size: var(--text-8);
  font-weight: 500;
  color: var(--muted-foreground);
  margin-bottom: 0.25rem;
}
.ap-row { display: flex; gap: 0.5rem; margin-top: 0.25rem; }
.ap-row select,
.ap-row input { margin-block-start: 0; }
.gen-chip {
  flex: 1;
  display: inline-flex;
  align-items: center;
  gap: 0.5rem;
  height: 2.25rem;
  padding: 0 0.75rem;
  border-radius: var(--radius-medium);
  background: color-mix(in srgb, var(--primary) 12%, transparent);
  border: 1px solid color-mix(in srgb, var(--primary) 30%, transparent);
  font-size: var(--text-7);
  color: var(--muted-foreground);
}
.gen-spark { color: var(--primary); flex-shrink: 0; }
.link { margin-top: 0.25rem; font-size: 11px; color: var(--faint-foreground); }
.link:hover { color: var(--muted-foreground); }
.ap-err { margin-top: 0.25rem; font-size: 11px; color: var(--danger); }
.ap-hint { margin-top: 0.25rem; font-size: 11px; color: var(--faint-foreground); }
.ap-hidden { display: none; }
.spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
