<script setup lang="ts">
// DashAlerts — understaffed / past-SLA banners at the top of the dashboard.
// The first two show; the rest sit behind a "+N more alerts" button.
// Each alert can be snoozed for 15 minutes (per browser, kept in localStorage).
import { computed, ref } from "vue";
import { RouterLink } from "vue-router";
import { alertCopy, alertKey, SNOOZE_MS, visibleAlerts, type DashAlert } from "../../lib/liveDashboard";

const props = defineProps<{ alerts: DashAlert[]; nowMs: number }>();

const STORE_KEY = "tring.dash.snoozed";

function load(): Record<string, number> {
  try {
    const raw = JSON.parse(localStorage.getItem(STORE_KEY) ?? "{}");
    const out: Record<string, number> = {};
    for (const [k, v] of Object.entries(raw ?? {})) if (typeof v === "number" && v > Date.now()) out[k] = v;
    return out;
  } catch {
    return {};
  }
}

const snoozed = ref<Record<string, number>>(load());

function snooze(a: DashAlert) {
  const next: Record<string, number> = {};
  for (const [k, v] of Object.entries(snoozed.value)) if (v > props.nowMs) next[k] = v;
  next[alertKey(a)] = props.nowMs + SNOOZE_MS;
  snoozed.value = next;
  try {
    localStorage.setItem(STORE_KEY, JSON.stringify(next));
  } catch {
    // storage unavailable: the snooze lasts until reload
  }
}

const SHOWN = 2;
const expanded = ref(false);
const live = computed(() => visibleAlerts(props.alerts, snoozed.value, props.nowMs));
const visible = computed(() => (expanded.value ? live.value : live.value.slice(0, SHOWN)));
const hidden = computed(() => live.value.length - visible.value.length);
</script>

<template>
  <div v-if="live.length" class="vstack gap-2">
    <div v-for="a in visible" :key="alertKey(a)" role="alert" data-variant="danger" class="justify-between">
      <div>
        <div><strong>{{ alertCopy(a).strong }}</strong>{{ alertCopy(a).rest }}</div>
        <div v-if="alertCopy(a).sub">{{ alertCopy(a).sub }}</div>
      </div>
      <div class="hstack gap-2">
        <RouterLink :to="{ name: 'team', params: { name: a.team } }">View team</RouterLink>
        <button
          type="button"
          class="ghost small"
          aria-label="Snooze alert for 15 minutes"
          title="Hide for 15 minutes"
          @click="snooze(a)"
        >
          ×
        </button>
      </div>
    </div>
    <div v-if="hidden > 0">
      <button type="button" class="ghost" @click="expanded = true">+{{ hidden }} more alerts</button>
    </div>
  </div>
</template>
