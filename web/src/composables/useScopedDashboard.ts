import { ref, onMounted, onUnmounted } from "vue";
import { api } from "../api/client";
import type { Call, Agent, Queues, StatsOverview } from "../api/types";
import { deriveStatus, type Snapshot } from "./useDashboardStream";

const POLL_MS = 5000;

// The dashboard for an agent or team manager: their role-scoped snapshot from
// /v1/me/dashboard (the tenant-wide SSE stream is for admins and supervisors),
// polled. Same refs as useDashboardStream so DashboardBoard takes either. The
// next poll is scheduled only after the previous one settles, so slow
// responses never stack.
export function useScopedDashboard() {
  const calls = ref<Call[]>([]);
  const agents = ref<Agent[]>([]);
  const queues = ref<Queues>({ total: 0, on_call: 0, queues: [] });
  const stats = ref<StatsOverview | null>(null);
  const ready = ref(false);
  let timer: number | undefined;
  let unmounted = false;

  async function load() {
    try {
      const s = await api.get<Snapshot>("/v1/me/dashboard");
      if (unmounted) return;
      calls.value = s.calls ?? [];
      agents.value = (s.agents ?? []).map((a) => ({ ...a, status: deriveStatus(a) }));
      queues.value = s.queues ?? { total: 0, on_call: 0, queues: [] };
      stats.value = s.stats ?? null;
      ready.value = true;
    } catch {
      /* transient; the next tick retries */
    }
    if (!unmounted) timer = window.setTimeout(load, POLL_MS);
  }

  onMounted(load);
  onUnmounted(() => {
    unmounted = true;
    clearTimeout(timer);
  });
  return { calls, agents, queues, stats, ready };
}
