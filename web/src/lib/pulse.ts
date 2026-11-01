import type { Directive } from "vue";

// v-pulse="active": a slow opacity pulse on the element while `active` is true.
// Uses the Web Animations API, so no stylesheet is involved; skipped entirely
// when the viewer asks for reduced motion.
const running = new WeakMap<HTMLElement, Animation>();

function sync(el: HTMLElement, active: boolean): void {
  const current = running.get(el);
  if (!active || window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) {
    current?.cancel();
    running.delete(el);
    return;
  }
  if (current || typeof el.animate !== "function") return;
  running.set(el, el.animate([{ opacity: 1 }, { opacity: 0.45 }, { opacity: 1 }], { duration: 1600, iterations: Infinity, easing: "ease-in-out" }));
}

export const vPulse: Directive<HTMLElement, boolean> = {
  mounted: (el, b) => sync(el, b.value),
  updated: (el, b) => sync(el, b.value),
  unmounted: (el) => sync(el, false),
};
