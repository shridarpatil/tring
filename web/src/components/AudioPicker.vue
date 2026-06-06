<script setup lang="ts">
import { ref, onMounted, computed } from "vue";
import { api } from "../api/client";
import type { Audio } from "../api/types";

// Reusable sound selector: v-models a path string (an asset's fs_path, "" for
// default, or a raw path via the advanced escape hatch). Used for IVR node
// audio and the system hold music — upload once in Sounds, pick it anywhere.
const props = defineProps<{
  modelValue: string;
  label?: string;
  defaultLabel?: string; // text for the empty option (e.g. "Use text-to-speech" / "Default")
}>();
const emit = defineEmits<{ (e: "update:modelValue", v: string): void }>();

const sounds = ref<Audio[]>([]);
const custom = ref(false);

onMounted(async () => {
  try {
    sounds.value = await api.get<Audio[]>("/v1/audio");
  } catch {
    /* leave empty — the custom-path escape hatch still works */
  }
  // A current value that isn't a known asset (and isn't empty) is a custom path.
  if (props.modelValue && !sounds.value.some((s) => s.fs_path === props.modelValue)) {
    custom.value = true;
  }
});

const val = computed({
  get: () => props.modelValue,
  set: (v: string) => emit("update:modelValue", v),
});
</script>

<template>
  <div>
    <span v-if="label" class="text-xs font-medium text-gray-700">{{ label }}</span>
    <template v-if="!custom">
      <select v-model="val" class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm">
        <option value="">{{ defaultLabel || "Default" }}</option>
        <option v-for="s in sounds" :key="s.id" :value="s.fs_path">{{ s.name }} ({{ s.format }})</option>
      </select>
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
    <p v-if="!sounds.length && !custom" class="text-[11px] text-gray-400 mt-1">
      No sounds uploaded yet — add some under <strong>Sounds</strong>.
    </p>
  </div>
</template>
