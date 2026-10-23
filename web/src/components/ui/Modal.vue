<script setup lang="ts">
import { ref, watch, nextTick, onMounted, onBeforeUnmount } from "vue";

// Native <dialog> styled by Oat's dialog.css (header / body / footer). The
// browser handles focus trapping, focus restore, Escape and the backdrop.
const props = withDefaults(
  defineProps<{ open: boolean; title?: string; size?: "sm" | "md" | "lg" | "detail" | "wide" }>(),
  { size: "md" },
);
const emit = defineEmits<{ (e: "update:open", v: boolean): void }>();

const dlg = ref<HTMLDialogElement | null>(null);
// Content stays mounted through Oat's 150ms close transition, then unmounts so
// forms start fresh on the next open.
const rendered = ref(props.open);
let unmountTimer: ReturnType<typeof setTimeout> | undefined;

async function sync(open: boolean) {
  const d = dlg.value;
  if (!d) return;
  if (open) {
    clearTimeout(unmountTimer);
    rendered.value = true;
    await nextTick(); // render content first so showModal() focuses its first field
    if (props.open && !d.open) d.showModal();
    document.body.style.overflow = "hidden";
  } else {
    if (d.open) d.close();
    document.body.style.overflow = "";
    unmountTimer = setTimeout(() => (rendered.value = false), 150);
  }
}

watch(() => props.open, sync);
onMounted(() => props.open && sync(true));
onBeforeUnmount(() => {
  clearTimeout(unmountTimer);
  document.body.style.overflow = "";
});

// Escape, or any close not driven by the `open` prop.
function onClose() {
  if (props.open) emit("update:open", false);
}

// A press whose target is the <dialog> itself landed on the backdrop.
function onMousedown(e: MouseEvent) {
  if (e.target === dlg.value) emit("update:open", false);
}
</script>

<template>
  <dialog ref="dlg" :class="`sz-${size}`" :aria-label="title" @close="onClose" @mousedown="onMousedown">
    <template v-if="rendered">
      <header v-if="title || $slots.title">
        <h3><slot name="title">{{ title }}</slot></h3>
      </header>
      <div class="modal-body"><slot /></div>
      <footer v-if="$slots.footer"><slot name="footer" /></footer>
    </template>
  </dialog>
</template>

<style scoped>
/* Sizing + a scrollable body; everything else is Oat's dialog. */
dialog[open] { display: flex; flex-direction: column; }
.modal-body { min-height: 0; }
.sz-sm { width: min(100% - 2rem, 24rem); }
.sz-md { width: min(100% - 2rem, 32rem); }
.sz-lg { width: min(100% - 2rem, 42rem); }
.sz-detail { width: min(100% - 2rem, 480px); }
.sz-wide { width: min(100% - 2rem, 56rem); }
</style>
