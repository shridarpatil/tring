import { ref, onMounted, onUnmounted } from "vue";
import { streamEvents } from "../lib/sse";
import type { Call, Agent, Queues, StatsOverview } from "../api/types";

interface Snapshot {
  calls: Call[];
  agents: Agent[];
  queues: Queues;
  stats: StatsOverview;
}

// Singleton reactive state — one SSE connection backs every consumer (the
// dashboard + the shell's LiveStatusStrip via useQueues), ref-counted.
const calls = ref<Call[]>([]);
const agents = ref<Agent[]>([]);
const queues = ref<Queues>({ total: 0, on_call: 0, queues: [] });
const stats = ref<StatsOverview | null>(null);
const ready = ref(false);
const connected = ref(false);

let stop: (() => void) | null = null;
let consumers = 0;

// Gateway publishes raw presence (online/on_call/offline); fold in the backend
// availability flag so connected-but-unavailable reads "away".
function deriveStatus(a: Agent): Agent["status"] {
  const raw = a.status === "away" ? "online" : a.status;
  if (raw === "online" && a.available_for_calls === false) return "away";
  return raw;
}

function onEvent(type: string, data: unknown) {
  switch (type) {
    case "snapshot": {
      const s = data as Snapshot;
      calls.value = s.calls ?? [];
      agents.value = (s.agents ?? []).map((a) => ({ ...a, status: deriveStatus(a) }));
      queues.value = s.queues ?? { total: 0, on_call: 0, queues: [] };
      stats.value = s.stats ?? null;
      ready.value = true;
      break;
    }
    case "call.ringing": {
      const c = data as Call;
      const i = calls.value.findIndex((x) => x.id === c.id);
      if (i >= 0) calls.value[i] = { ...calls.value[i], ...c };
      else calls.value = [...calls.value, c];
      break;
    }
    case "call.answered": {
      const d = data as { call_id: string; agent_id?: string; answered_at?: string };
      calls.value = calls.value.map((c) =>
        c.id === d.call_id ? { ...c, status: "answered", agent_id: d.agent_id, answered_at: d.answered_at } : c,
      );
      break;
    }
    case "call.ended": {
      const d = data as { call_id: string };
      calls.value = calls.value.filter((c) => c.id !== d.call_id);
      break;
    }
    case "agent.presence": {
      const d = data as { sip_username: string; status: Agent["status"] };
      agents.value = agents.value.map((a) =>
        a.sip_username === d.sip_username ? { ...a, status: deriveStatus({ ...a, status: d.status }) } : a,
      );
      break;
    }
    case "agent.status": {
      const d = data as { sip_username: string; available_for_calls: boolean };
      agents.value = agents.value.map((a) =>
        a.sip_username === d.sip_username
          ? { ...a, available_for_calls: d.available_for_calls, status: deriveStatus({ ...a, available_for_calls: d.available_for_calls }) }
          : a,
      );
      break;
    }
    case "queues": {
      queues.value = data as Queues;
      break;
    }
    case "stats": {
      stats.value = data as StatsOverview;
      break;
    }
  }
}

export function useDashboardStream() {
  onMounted(() => {
    consumers++;
    if (consumers === 1) {
      stop = streamEvents("/v1/stream/dashboard", {
        onEvent,
        onOpen: () => (connected.value = true),
        onError: () => (connected.value = false),
      });
    }
  });
  onUnmounted(() => {
    consumers--;
    if (consumers === 0 && stop) {
      stop();
      stop = null;
      ready.value = false;
      connected.value = false;
    }
  });
  return { calls, agents, queues, stats, ready, connected };
}
