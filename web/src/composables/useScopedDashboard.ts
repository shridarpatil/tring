import { ref, onMounted, onUnmounted } from "vue";
import { api } from "../api/client";
import type { Call, Agent, Queues, StatsOverview } from "../api/types";
import { errMessage } from "../lib/errors";
import { pollWantsStats } from "../lib/me";
import { toast } from "../lib/toast";
import { deriveStatus, type Snapshot } from "./useDashboardStream";

const POLL_MS = 5000;

// The dashboard for an agent or team manager: their role-scoped snapshot from
// /v1/me/dashboard (the tenant-wide SSE stream is for admins and supervisors),
// polled. Same refs as useDashboardStream so DashboardBoard takes either.
// - The live panels refresh every poll; the day's stats (several aggregate
//   queries) only every 6th, via ?stats=0 in between.
// - The next poll is scheduled only after the previous one settles, so slow
//   responses never stack.
// - A hidden tab doesn't poll; it refreshes as soon as it is shown again.
export function useScopedDashboard() {
  const calls = ref<Call[]>([]);
  const agents = ref<Agent[]>([]);
  const queues = ref<Queues>({ total: 0, on_call: 0, queues: [] });
  const stats = ref<StatsOverview | null>(null);
  const ready = ref(false);
  let timer: number | undefined;
  let inFlight = false;
  let polls = 0;
  let warned = false;
  let unmounted = false;

  async function load() {
    clearTimeout(timer);
    timer = undefined;
    if (unmounted || inFlight || document.hidden) return;
    inFlight = true;
    const withStats = pollWantsStats(polls);
    try {
      const s = await api.get<Partial<Snapshot>>(withStats ? "/v1/me/dashboard" : "/v1/me/dashboard?stats=0");
      if (unmounted) return;
      polls++;
      calls.value = s.calls ?? [];
      agents.value = (s.agents ?? []).map((a) => ({ ...a, status: deriveStatus(a) }));
      queues.value = s.queues ?? { total: 0, on_call: 0, queues: [] };
      if (withStats) stats.value = s.stats ?? null;
      ready.value = true;
      warned = false;
    } catch (e) {
      // Once per outage, so a persistent failure isn't a silent "Loading…".
      if (!warned && !unmounted) toast.error(errMessage(e, "could not load the dashboard"));
      warned = true;
    } finally {
      inFlight = false;
    }
    if (!unmounted && !document.hidden) timer = window.setTimeout(load, POLL_MS);
  }

  function onVisibility() {
    if (!document.hidden) load();
  }

  onMounted(() => {
    document.addEventListener("visibilitychange", onVisibility);
    load();
  });
  onUnmounted(() => {
    unmounted = true;
    clearTimeout(timer);
    document.removeEventListener("visibilitychange", onVisibility);
  });
  return { calls, agents, queues, stats, ready };
}
