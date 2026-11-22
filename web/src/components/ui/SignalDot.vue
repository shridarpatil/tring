<script setup lang="ts">
import { computed } from "vue";
import { ClipboardPen } from "lucide-vue-next";

// Signature component: live presence/call state as a first-class signal.
// on_call / ringing pulse a radiating ring (disabled under reduced-motion).
const props = withDefaults(
  defineProps<{
    state?: "online" | "on_call" | "away" | "wrap_up" | "offline" | "ringing";
    label?: string;
    hideLabel?: boolean;
    size?: "sm" | "md";
  }>(),
  { state: "offline", size: "md" },
);

const colorVar = computed(
  () =>
    ({
      online: "var(--color-online)",
      on_call: "var(--color-oncall)",
      away: "var(--color-away)",
      wrap_up: "var(--color-away)",
      offline: "var(--color-offline)",
      ringing: "var(--primary)",
    })[props.state],
);

const pulse = computed(() => props.state === "on_call" || props.state === "ringing");

const defaultText = computed(
  () =>
    ({
      online: "Online",
      on_call: "On call",
      away: "Away",
      wrap_up: "Wrap-up",
      offline: "Offline",
      ringing: "Ringing",
    })[props.state],
);
const text = computed(() => props.label ?? defaultText.value);
</script>

<template>
  <span class="sig" :style="{ '--dot': colorVar }">
    <ClipboardPen v-if="state === 'wrap_up'" class="wrap-icon" :class="`sz-${size}`" aria-hidden="true" />
    <span v-else class="dot-wrap" :class="`sz-${size}`">
      <span v-if="pulse" class="ring"></span>
      <span class="dot"></span>
    </span>
    <span v-if="!hideLabel" class="lbl">{{ text }}</span>
  </span>
</template>

<style scoped>
.sig { display: inline-flex; align-items: center; gap: 0.5rem; }
.dot-wrap { position: relative; display: inline-flex; }
.sz-sm { height: 0.5rem; width: 0.5rem; }
.sz-md { height: 0.625rem; width: 0.625rem; }
.wrap-icon { color: var(--dot); }
.wrap-icon.sz-sm { height: 0.75rem; width: 0.75rem; }
.wrap-icon.sz-md { height: 0.875rem; width: 0.875rem; }
.dot {
  position: relative;
  display: inline-flex;
  height: 100%;
  width: 100%;
  border-radius: 9999px;
  background: var(--dot);
}
.ring {
  position: absolute;
  inset: 0;
  display: inline-flex;
  border-radius: 9999px;
  background: var(--dot);
  opacity: 0.6;
  animation: tring-pulse-ring 1.8s ease-out infinite;
}
.lbl { font-size: var(--text-8); color: var(--muted-foreground); }
@media (prefers-reduced-motion: reduce) {
  .ring { animation: none; }
}
</style>
