<script setup lang="ts">
// ScheduleEditor — weekly open/close hours, one row per day. Same shape as the
// IVR Timing node. An empty schedule means "always open".
import { computed } from "vue";
import type { TeamDay } from "../api/types";

const props = defineProps<{ modelValue: TeamDay[] }>();
const emit = defineEmits<{ (e: "update:modelValue", v: TeamDay[]): void }>();

const DAYS = ["monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"];
const defaultWeek = (): TeamDay[] =>
  DAYS.map((day) => ({
    day,
    enabled: day !== "saturday" && day !== "sunday",
    start_time: "09:00",
    end_time: "17:00",
  }));

const alwaysOpen = computed(() => props.modelValue.length === 0);
const rows = computed(() => (alwaysOpen.value ? defaultWeek() : props.modelValue));

function setAlwaysOpen(on: boolean) {
  emit("update:modelValue", on ? [] : defaultWeek());
}

function patch(i: number, change: Partial<TeamDay>) {
  emit(
    "update:modelValue",
    props.modelValue.map((d, k) => (k === i ? { ...d, ...change } : d)),
  );
}

const label = (day: string) => day.charAt(0).toUpperCase() + day.slice(1);
</script>

<template>
  <div class="vstack gap-2">
    <label class="hstack gap-2">
      <input type="checkbox" :checked="alwaysOpen" @change="setAlwaysOpen(($event.target as HTMLInputElement).checked)" />
      Always open
    </label>
    <div class="table">
      <table>
        <tbody>
          <tr v-for="(d, i) in rows" :key="d.day">
            <td>
              <label class="hstack gap-2">
                <input
                  type="checkbox"
                  :checked="d.enabled"
                  :disabled="alwaysOpen"
                  :aria-label="`${label(d.day)} open`"
                  @change="patch(i, { enabled: ($event.target as HTMLInputElement).checked })"
                />
                {{ label(d.day) }}
              </label>
            </td>
            <td>
              <input
                type="time"
                :value="d.start_time"
                :disabled="alwaysOpen || !d.enabled"
                :aria-label="`${label(d.day)} opens`"
                @input="patch(i, { start_time: ($event.target as HTMLInputElement).value })"
              />
            </td>
            <td>
              <input
                type="time"
                :value="d.end_time"
                :disabled="alwaysOpen || !d.enabled"
                :aria-label="`${label(d.day)} closes`"
                @input="patch(i, { end_time: ($event.target as HTMLInputElement).value })"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
