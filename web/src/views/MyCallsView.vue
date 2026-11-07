<script setup lang="ts">
import { ref, computed, watch, onMounted } from "vue";
import { api } from "../api/client";
import { errMessage } from "../lib/errors";
import { toast } from "../lib/toast";
import { CALL_RANGES, type CallRange } from "../lib/me";
import type { Call, Paginated } from "../api/types";
import { Toggle, CallsTable, Pagination } from "../components/ui";
import { useAgentWidget } from "../composables/useAgentWidget";

// The user's own call history: calls they answered (with recordings) and calls
// that rang them and they missed (badged, no recording). Also mounts the call
// panel, so admins (who land on /admin) can take calls here.
const { available, savingAvail, setAvailable } = useAgentWidget();

const PAGE = 25;
const range = ref<CallRange>("today");
const calls = ref<Call[]>([]);
const total = ref(0);
const offset = ref(0);
const loading = ref(true);
// Each load is tagged so a slow response for an old range/page can't land last.
let seq = 0;

async function load() {
  const mine = ++seq;
  loading.value = true;
  try {
    const res = await api.get<Paginated<Call>>(`/v1/me/calls?range=${range.value}&limit=${PAGE}&offset=${offset.value}`);
    if (mine !== seq) return;
    calls.value = res.items;
    total.value = res.total;
  } catch (e) {
    if (mine === seq) toast.error(errMessage(e, "could not load calls"));
  } finally {
    if (mine === seq) loading.value = false;
  }
}
const page = computed(() => Math.floor(offset.value / PAGE) + 1);
function setPage(n: number) {
  offset.value = (n - 1) * PAGE;
  load();
}
watch(range, () => {
  offset.value = 0;
  load();
});
onMounted(load);
</script>

<template>
  <div class="view">
    <div class="row-between">
      <h1 class="page-title">My calls</h1>
      <Toggle
        :model-value="available"
        :disabled="savingAvail"
        label="Available for calls"
        @update:model-value="setAvailable"
      />
    </div>

    <div class="hstack gap-1" role="group" aria-label="Calls in range">
      <button
        v-for="r in CALL_RANGES"
        :key="r.key"
        type="button"
        class="small"
        :class="{ outline: range !== r.key }"
        :aria-pressed="range === r.key"
        @click="range = r.key"
      >
        {{ r.label }}
      </button>
    </div>

    <div class="card" style="padding: 0">
      <CallsTable :calls="calls" :loading="loading" hide-agent />
      <Pagination :page="page" :page-size="PAGE" :total="total" label="calls" @update:page="setPage" />
    </div>
  </div>
</template>
