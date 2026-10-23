import { reactive } from "vue";

// App-level confirm dialog, replacing window.confirm(). A reactive singleton,
// mirroring lib/toast.ts; ConfirmDialog.vue (mounted once in App.vue) renders
// it. Callers await the answer: `if (!(await confirmDialog("…"))) return;`

export type ConfirmOptions = {
  title?: string;
  confirmLabel?: string;
};

const state = reactive<{
  open: boolean;
  message: string;
  title: string;
  confirmLabel: string;
}>({ open: false, message: "", title: "", confirmLabel: "" });

let resolver: ((ok: boolean) => void) | null = null;

export const confirmState = state;

export function confirmDialog(message: string, opts: ConfirmOptions = {}): Promise<boolean> {
  resolver?.(false); // a newer prompt supersedes an unanswered one
  state.message = message;
  state.title = opts.title ?? "Are you sure?";
  state.confirmLabel = opts.confirmLabel ?? "Delete";
  state.open = true;
  return new Promise((resolve) => (resolver = resolve));
}

export function settleConfirm(ok: boolean) {
  state.open = false;
  resolver?.(ok);
  resolver = null;
}
