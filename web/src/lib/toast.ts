import { reactive } from "vue";

// App-level toast bus. A reactive singleton, mirroring the stores/auth.ts
// pattern — no store library. Views call toast.error(...) for action-level
// outcomes; field-level validation stays on the input's `error` prop.

export type ToastTone = "success" | "danger" | "info";

export type ToastItem = {
  id: number;
  tone: ToastTone;
  message: string;
};

const state = reactive<{ items: ToastItem[] }>({ items: [] });
let seq = 0;

function push(tone: ToastTone, message: string, ttl = 5000) {
  const id = ++seq;
  state.items.push({ id, tone, message });
  if (ttl > 0) setTimeout(() => dismiss(id), ttl);
  return id;
}

function dismiss(id: number) {
  const i = state.items.findIndex((t) => t.id === id);
  if (i !== -1) state.items.splice(i, 1);
}

export const toasts = state;

export const toast = {
  success: (message: string, ttl?: number) => push("success", message, ttl),
  error: (message: string, ttl?: number) => push("danger", message, ttl ?? 7000),
  info: (message: string, ttl?: number) => push("info", message, ttl),
  dismiss,
};
