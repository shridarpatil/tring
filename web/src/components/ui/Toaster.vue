<script setup lang="ts">
import { CheckCircle2, AlertCircle, Info, X } from "lucide-vue-next";
import { toasts, toast } from "../../lib/toast";

const icon = { success: CheckCircle2, danger: AlertCircle, info: Info };
</script>

<template>
  <Teleport to="body">
    <div class="toaster">
      <TransitionGroup name="toast">
        <div v-for="t in toasts.items" :key="t.id" class="card toast-row" role="status">
          <component :is="icon[t.tone]" :size="16" class="toast-icon" :class="`tone-${t.tone}`" />
          <p class="toast-msg">{{ t.message }}</p>
          <button
            type="button"
            class="ghost icon small"
            aria-label="Dismiss"
            @click="toast.dismiss(t.id)"
          >
            <X :size="14" />
          </button>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>

<style scoped>
.toaster {
  position: fixed;
  top: 1rem;
  right: 1rem;
  z-index: 60;
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  width: 20rem;
  max-width: calc(100vw - 2rem);
}
.toast-row {
  display: flex;
  align-items: flex-start;
  gap: 0.75rem;
  padding: 0.75rem 1rem;
  box-shadow: var(--shadow-large);
}
.toast-icon { margin-top: 0.125rem; flex-shrink: 0; }
.tone-success { color: var(--success); }
.tone-danger { color: var(--danger); }
.tone-info { color: var(--primary); }
.toast-msg { flex: 1; font-size: var(--text-7); word-break: break-word; }
.toast-enter-active,
.toast-leave-active { transition: all 0.2s ease; }
.toast-enter-from,
.toast-leave-to { opacity: 0; transform: translateX(1rem); }
.toast-move { transition: transform 0.2s ease; }
</style>
