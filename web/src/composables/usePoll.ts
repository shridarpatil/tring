import { onMounted, onUnmounted } from "vue";

// usePoll runs fn once on mount (unless immediate=false) and then every
// intervalMs, clearing the timer on unmount. Replaces the hand-rolled
// setInterval/clearInterval lifecycle duplicated in the live views.
export function usePoll(fn: () => void, intervalMs: number, opts: { immediate?: boolean } = {}) {
  let timer: number | undefined;
  onMounted(() => {
    if (opts.immediate !== false) fn();
    timer = window.setInterval(fn, intervalMs);
  });
  onUnmounted(() => {
    if (timer) clearInterval(timer);
  });
}
