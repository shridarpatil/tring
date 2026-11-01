<script setup lang="ts">
// DashAlerts — understaffed / past-SLA banners at the top of the dashboard.
// The first two show; the rest sit behind a "+N more alerts" button.
import { computed, ref } from "vue";
import { RouterLink } from "vue-router";
import { alertCopy, type DashAlert } from "../../lib/liveDashboard";

const props = defineProps<{ alerts: DashAlert[] }>();

const SHOWN = 2;
const expanded = ref(false);
const visible = computed(() => (expanded.value ? props.alerts : props.alerts.slice(0, SHOWN)));
const hidden = computed(() => props.alerts.length - visible.value.length);
</script>

<template>
  <div v-if="alerts.length" class="vstack gap-2">
    <div v-for="a in visible" :key="a.team + a.kind" role="alert" data-variant="danger" class="justify-between">
      <div>
        <div><strong>{{ alertCopy(a).strong }}</strong>{{ alertCopy(a).rest }}</div>
        <div v-if="alertCopy(a).sub">{{ alertCopy(a).sub }}</div>
      </div>
      <RouterLink :to="{ name: 'team', params: { name: a.team } }">View team</RouterLink>
    </div>
    <div v-if="hidden > 0">
      <button type="button" class="ghost" @click="expanded = true">+{{ hidden }} more alerts</button>
    </div>
  </div>
</template>
