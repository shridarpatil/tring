<script setup lang="ts">
import { ref, watch, nextTick, onBeforeUnmount } from "vue";
import { X } from "lucide-vue-next";

const props = withDefaults(
  defineProps<{ open: boolean; title?: string; size?: "sm" | "md" | "lg" | "detail" | "wide" }>(),
  { size: "md" },
);
const emit = defineEmits<{ (e: "update:open", v: boolean): void }>();

const panel = ref<HTMLElement | null>(null);
let lastFocused: HTMLElement | null = null;

const FOCUSABLE =
  'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';

function close() {
  emit("update:open", false);
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === "Escape") {
    e.preventDefault();
    close();
    return;
  }
  if (e.key === "Tab" && panel.value) {
    const f = Array.from(panel.value.querySelectorAll<HTMLElement>(FOCUSABLE));
    if (f.length === 0) return;
    const first = f[0];
    const last = f[f.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }
}

watch(
  () => props.open,
  async (v) => {
    if (v) {
      lastFocused = document.activeElement as HTMLElement;
      document.addEventListener("keydown", onKeydown);
      document.body.style.overflow = "hidden";
      await nextTick();
      panel.value?.querySelector<HTMLElement>(FOCUSABLE)?.focus();
    } else {
      document.removeEventListener("keydown", onKeydown);
      document.body.style.overflow = "";
      lastFocused?.focus();
    }
  },
);

onBeforeUnmount(() => {
  document.removeEventListener("keydown", onKeydown);
  document.body.style.overflow = "";
});
</script>

<template>
  <Teleport to="body">
    <Transition name="modal">
      <div v-if="open" class="scrim-wrap">
        <div class="scrim" @click="close"></div>
        <div
          ref="panel"
          role="dialog"
          aria-modal="true"
          :aria-label="title"
          class="card modal-panel"
          :class="`sz-${size}`"
        >
          <div v-if="title || $slots.title" class="hstack justify-between modal-head">
            <h2 class="modal-title"><slot name="title">{{ title }}</slot></h2>
            <button type="button" class="ghost icon small" aria-label="Close" @click="close">
              <X :size="16" />
            </button>
          </div>
          <div class="modal-body"><slot /></div>
          <div v-if="$slots.footer" class="hstack justify-end modal-foot">
            <slot name="footer" />
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
/* Structural only — Oat has no teleported-overlay modal. Colours via tokens. */
.scrim-wrap {
  position: fixed;
  inset: 0;
  z-index: 50;
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding: 1.5rem;
  overflow-y: auto;
}
.scrim {
  position: fixed;
  inset: 0;
  background: color-mix(in srgb, var(--overlay, #0f172a) 70%, transparent);
  backdrop-filter: blur(2px);
}
.modal-panel {
  position: relative;
  z-index: 10;
  width: 100%;
  margin-top: 6vh;
  padding: 0;
  box-shadow: var(--shadow-large);
}
.sz-sm { max-width: 24rem; }
.sz-md { max-width: 32rem; }
.sz-lg { max-width: 42rem; }
.sz-detail { max-width: 480px; }
.sz-wide { max-width: 56rem; }
.modal-head { height: 3rem; padding: 0 1.25rem; border-bottom: 1px solid var(--border); }
.modal-title { font-size: var(--text-7); font-weight: 500; }
.modal-body { padding: 1.25rem; }
.modal-foot { padding: 0.75rem 1.25rem; border-top: 1px solid var(--border); background: var(--secondary); }
.modal-enter-active,
.modal-leave-active { transition: opacity 0.15s ease; }
.modal-enter-from,
.modal-leave-to { opacity: 0; }
</style>
