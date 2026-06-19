<script setup lang="ts">
import { ref, onMounted, computed } from "vue";
import { errMessage } from "../lib/errors";
import { usePoll } from "../composables/usePoll";
import { api } from "../api/client";
import type { Tenant, Call, Agent, PhoneNumber, Queues } from "../api/types";
import { Card, Panel, DataTable, type Column, SignalDot, LevelMeter, Badge } from "../components/ui";
import { callStatusTone } from "../lib/status";

const tenant = ref<Tenant | null>(null);
const calls = ref<Call[]>([]);
const agents = ref<Agent[]>([]);
const numbers = ref<PhoneNumber[]>([]);
const queues = ref<Queues>({ total: 0, on_call: 0, queues: [] });
const loading = ref(true);
const error = ref("");

// Teams with at least one caller waiting, for the breakdown under the card.
const waitingTeams = computed(() => queues.value.queues.filter((q) => q.waiting > 0));

async function loadQueues() {
  try {
    queues.value = await api.get<Queues>("/v1/queues");
  } catch {
    /* leave last value; the queue count is best-effort */
  }
}

onMounted(async () => {
  try {
    const [t, c, a, n] = await Promise.all([
      api.get<Tenant>("/v1/tenants/me"),
      api.get<Call[]>("/v1/calls?limit=8"),
      api.get<Agent[]>("/v1/agents"),
      api.get<PhoneNumber[]>("/v1/numbers"),
    ]);
    tenant.value = t;
    calls.value = c;
    agents.value = a;
    numbers.value = n;
  } catch (e) {
    error.value = errMessage(e, "failed to load");
  } finally {
    loading.value = false;
  }
});

// Live queue depth + on-call: re-poll every 5s (loads immediately on mount).
usePoll(loadQueues, 5000);

const activeOutbound = computed(
  () => numbers.value.filter((n) => n.type === "outbound" && n.active).length,
);

const cols: Column[] = [
  { key: "from_number", label: "From", mono: true },
  { key: "to_number", label: "To" },
  { key: "status", label: "Status" },
  { key: "started_at", label: "Started", align: "right" },
];
</script>

<template>
  <div class="space-y-6">
    <h1 class="text-xl font-semibold tracking-tight">Dashboard</h1>
    <p v-if="error" class="text-sm text-danger">{{ error }}</p>
    <div v-if="loading" class="text-ink-faint text-sm">Loading…</div>

    <template v-else>
      <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-4">
        <!-- On-call: the signature live card -->
        <Card>
          <div class="flex items-center justify-between">
            <div class="text-3xl font-semibold tabular-nums" :class="queues.on_call > 0 ? 'text-live' : ''">
              {{ queues.on_call }}
            </div>
            <SignalDot :state="queues.on_call > 0 ? 'on_call' : 'offline'" hide-label />
            <LevelMeter v-if="queues.on_call > 0" active />
          </div>
          <div class="text-sm text-ink-muted mt-1">On call</div>
        </Card>

        <Card>
          <div class="text-3xl font-semibold tabular-nums" :class="queues.total > 0 ? 'text-warning' : ''">
            {{ queues.total }}
          </div>
          <div class="text-sm text-ink-muted mt-1">Calls in queue</div>
          <div v-if="waitingTeams.length" class="mt-2 space-y-0.5">
            <div
              v-for="q in waitingTeams"
              :key="q.team"
              class="text-xs text-warning flex justify-between gap-2"
            >
              <span class="truncate">{{ q.team }}</span>
              <span class="font-mono">{{ q.waiting }}</span>
            </div>
          </div>
        </Card>

        <Card>
          <div class="text-3xl font-semibold tabular-nums">{{ agents.length }}</div>
          <div class="text-sm text-ink-muted mt-1">Agents</div>
        </Card>
        <Card>
          <div class="text-3xl font-semibold tabular-nums">{{ numbers.length }}</div>
          <div class="text-sm text-ink-muted mt-1">Numbers</div>
        </Card>
        <Card>
          <div class="text-3xl font-semibold tabular-nums">{{ activeOutbound }}</div>
          <div class="text-sm text-ink-muted mt-1">Active outbound trunks</div>
        </Card>
        <Card>
          <div class="text-2xl font-semibold capitalize">{{ tenant?.outbound_strategy || "—" }}</div>
          <div class="text-sm text-ink-muted mt-1">Trunk strategy</div>
        </Card>
      </div>

      <Panel>
        <template #title>Recent calls</template>
        <DataTable :columns="cols" :rows="calls" empty-text="No calls yet">
          <template #cell-status="{ value }">
            <Badge :tone="callStatusTone(String(value))">{{ value }}</Badge>
          </template>
          <template #cell-started_at="{ value }">
            <span class="text-ink-muted">{{ new Date(String(value)).toLocaleString() }}</span>
          </template>
        </DataTable>
      </Panel>
    </template>
  </div>
</template>
