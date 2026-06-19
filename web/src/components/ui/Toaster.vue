<script setup lang="ts">
import { CheckCircle2, AlertCircle, Info, X } from "lucide-vue-next";
import { toasts, toast } from "../../lib/toast";

const icon = { success: CheckCircle2, danger: AlertCircle, info: Info };
const accent = {
  success: "text-success",
  danger: "text-danger",
  info: "text-accent",
};
</script>

<template>
  <Teleport to="body">
    <div class="fixed top-4 right-4 z-[60] flex flex-col gap-2 w-80 max-w-[calc(100vw-2rem)]">
      <TransitionGroup name="toast">
        <div
          v-for="t in toasts.items"
          :key="t.id"
          class="flex items-start gap-3 rounded-lg bg-surface border border-border shadow-xl px-4 py-3"
          role="status"
        >
          <component :is="icon[t.tone]" class="h-4 w-4 mt-0.5 shrink-0" :class="accent[t.tone]" />
          <p class="flex-1 text-sm text-ink break-words">{{ t.message }}</p>
          <button
            type="button"
            class="text-ink-faint hover:text-ink transition-colors"
            aria-label="Dismiss"
            @click="toast.dismiss(t.id)"
          >
            <X class="h-3.5 w-3.5" />
          </button>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>

<style scoped>
.toast-enter-active,
.toast-leave-active {
  transition: all 0.2s ease;
}
.toast-enter-from {
  opacity: 0;
  transform: translateX(1rem);
}
.toast-leave-to {
  opacity: 0;
  transform: translateX(1rem);
}
.toast-move {
  transition: transform 0.2s ease;
}
</style>
