import { ref, onMounted, onUnmounted } from "vue";
import { api } from "../api/client";
import type { Queues } from "../api/types";

// Shared live queue / on-call state. A SINGLE poller backs every consumer (the
// shell's LiveStatusStrip + the dashboard), so /v1/queues is fetched once per
// interval no matter how many components are mounted — previously the strip and
// the dashboard each polled it, doubling the request on the dashboard.
//
// Ref-counted: the poll starts on the first consumer's mount and stops when the
// last one unmounts. Best-effort — a failed poll keeps the last value.
const queues = ref<Queues>({ total: 0, on_call: 0, queues: [] });
let timer: number | undefined;
let consumers = 0;

async function load() {
  try {
    queues.value = await api.get<Queues>("/v1/queues");
  } catch {
    /* keep last value */
  }
}

export function useQueues(intervalMs = 5000) {
  onMounted(() => {
    consumers++;
    if (consumers === 1) {
      load();
      timer = window.setInterval(load, intervalMs);
    }
  });
  onUnmounted(() => {
    consumers--;
    if (consumers === 0 && timer) {
      clearInterval(timer);
      timer = undefined;
    }
  });
  return queues;
}
