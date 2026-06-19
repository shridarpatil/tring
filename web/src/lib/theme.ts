import { reactive } from "vue";

// Theme bus — a reactive singleton (same pattern as stores/auth.ts). Light is
// the default; the choice persists in localStorage and is applied to
// <html data-theme="…">, which re-points every design token (see style.css).

export type Theme = "light" | "dark";
const KEY = "tring_theme";

function initial(): Theme {
  return localStorage.getItem(KEY) === "dark" ? "dark" : "light";
}

function apply(t: Theme) {
  document.documentElement.setAttribute("data-theme", t);
}

const state = reactive({ theme: initial() });
// Apply on module load (imported early in main.ts) so there's no flash.
apply(state.theme);

export const theme = {
  state,
  toggle() {
    theme.set(state.theme === "dark" ? "light" : "dark");
  },
  set(t: Theme) {
    state.theme = t;
    localStorage.setItem(KEY, t);
    apply(t);
  },
};
