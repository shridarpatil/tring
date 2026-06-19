<script setup lang="ts">
import { ref, watch, nextTick, onBeforeUnmount } from "vue";
import { X } from "lucide-vue-next";

const props = withDefaults(
  defineProps<{ open: boolean; title?: string; size?: "sm" | "md" | "lg" }>(),
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

const sizeCls = { sm: "max-w-sm", md: "max-w-lg", lg: "max-w-2xl" };
</script>

<template>
  <Teleport to="body">
    <Transition name="modal">
      <div
        v-if="open"
        class="fixed inset-0 z-50 flex items-start justify-center p-4 sm:p-6 overflow-y-auto"
      >
        <div class="fixed inset-0 bg-overlay/70 backdrop-blur-sm" @click="close"></div>
        <div
          ref="panel"
          role="dialog"
          aria-modal="true"
          :aria-label="title"
          class="relative z-10 w-full mt-[6vh] rounded-xl bg-surface border border-border shadow-2xl"
          :class="sizeCls[size]"
        >
          <div
            v-if="title || $slots.title"
            class="flex items-center justify-between gap-3 px-5 h-12 border-b border-border"
          >
            <h2 class="text-sm font-medium text-ink"><slot name="title">{{ title }}</slot></h2>
            <button
              type="button"
              class="text-ink-faint hover:text-ink transition-colors"
              aria-label="Close"
              @click="close"
            >
              <X class="h-4 w-4" />
            </button>
          </div>
          <div class="p-5"><slot /></div>
          <div
            v-if="$slots.footer"
            class="flex items-center justify-end gap-2 px-5 py-3 border-t border-border bg-surface-2/40"
          >
            <slot name="footer" />
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.modal-enter-active,
.modal-leave-active {
  transition: opacity 0.15s ease;
}
.modal-enter-from,
.modal-leave-to {
  opacity: 0;
}
</style>
