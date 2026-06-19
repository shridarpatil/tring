<script setup lang="ts">
import { computed } from "vue";

// Signature component: live presence/call state as a first-class signal.
// on_call / ringing pulse a radiating ring (disabled under reduced-motion).
const props = withDefaults(
  defineProps<{
    state?: "online" | "on_call" | "away" | "offline" | "ringing";
    label?: string;
    hideLabel?: boolean;
    size?: "sm" | "md";
  }>(),
  { state: "offline", size: "md" },
);

const color = computed(
  () =>
    ({
      online: "bg-online",
      on_call: "bg-oncall",
      away: "bg-away",
      offline: "bg-offline",
      ringing: "bg-accent",
    })[props.state],
);

const pulse = computed(() => props.state === "on_call" || props.state === "ringing");
const dotSize = computed(() => (props.size === "sm" ? "h-2 w-2" : "h-2.5 w-2.5"));

const defaultText = computed(
  () =>
    ({
      online: "Online",
      on_call: "On call",
      away: "Away",
      offline: "Offline",
      ringing: "Ringing",
    })[props.state],
);
const text = computed(() => props.label ?? defaultText.value);
</script>

<template>
  <span class="inline-flex items-center gap-2">
    <span class="relative inline-flex" :class="dotSize">
      <span
        v-if="pulse"
        class="absolute inset-0 inline-flex rounded-full opacity-60 animate-pulse-ring"
        :class="color"
      ></span>
      <span class="relative inline-flex rounded-full" :class="[dotSize, color]"></span>
    </span>
    <span v-if="!hideLabel" class="text-xs text-ink-muted">{{ text }}</span>
  </span>
</template>
