import { useDashboardStream } from "./useDashboardStream";

// Backed by the shared dashboard SSE stream (no polling). Kept as a named
// composable so existing consumers (the shell's LiveStatusStrip) need no change.
export function useQueues() {
  const { queues } = useDashboardStream();
  return queues;
}
