<script setup lang="ts">
// CallOutcome — a call's wrap-up outcome (disposition, notes, tags). Read-only
// by default; `editable` (admins only) turns it into a form that PATCHes
// /v1/calls/{id}/wrap-up and emits the updated call.
import { ref, computed, watch } from "vue";
import { api } from "../../api/client";
import { errMessage } from "../../lib/errors";
import { toast } from "../../lib/toast";
import type { Call, WrapUpLabel } from "../../api/types";
import Badge from "./Badge.vue";
import Button from "./Button.vue";
import Select from "./Select.vue";
import Textarea from "./Textarea.vue";

const props = defineProps<{
  call: Call;
  editable?: boolean;
  dispositions: WrapUpLabel[];
  tags: WrapUpLabel[];
}>();
const emit = defineEmits<{ saved: [call: Call] }>();

const dispositionId = ref("");
const notes = ref("");
const tagIds = ref<string[]>([]);
const saving = ref(false);

function reset() {
  dispositionId.value = props.call.disposition?.id ?? "";
  notes.value = props.call.notes ?? "";
  tagIds.value = (props.call.tags ?? []).map((t) => t.id);
}
watch(() => props.call, reset, { immediate: true });

// Archived labels are offered only while the call still carries them.
const dispositionOptions = computed(() => [
  { value: "", label: "None" },
  ...props.dispositions
    .filter((d) => !d.archived || d.id === dispositionId.value)
    .map((d) => ({ value: d.id, label: d.archived ? `${d.name} (archived)` : d.name })),
]);
const tagOptions = computed(() => props.tags.filter((t) => !t.archived || tagIds.value.includes(t.id)));

async function save() {
  saving.value = true;
  try {
    const updated = await api.patch<Call>(`/v1/calls/${props.call.id}/wrap-up`, {
      disposition_id: dispositionId.value || null,
      notes: notes.value.trim() || null,
      tag_ids: tagIds.value,
    });
    emit("saved", updated);
    toast.success("Outcome saved");
  } catch (e) {
    toast.error(errMessage(e, "save failed"));
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <div>
    <div class="text-lighter" style="margin-top: 1rem; margin-bottom: 0.5rem; font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.08em">
      Outcome
    </div>
    <form v-if="editable" class="vstack" @submit.prevent="save">
      <Select v-model="dispositionId" label="Disposition" :options="dispositionOptions" />
      <Textarea v-model="notes" label="Notes" :rows="3" />
      <fieldset v-if="tagOptions.length" class="hstack" style="flex-wrap: wrap; border: 0; padding: 0; margin: 0">
        <legend class="text-light">Tags</legend>
        <label v-for="t in tagOptions" :key="t.id">
          <input v-model="tagIds" type="checkbox" :value="t.id" />
          {{ t.name }}{{ t.archived ? " (archived)" : "" }}
        </label>
      </fieldset>
      <div><Button type="submit" size="sm" :loading="saving">Save outcome</Button></div>
    </form>
    <div v-else class="vstack">
      <div>
        <Badge v-if="call.disposition" tone="accent">{{ call.disposition.name }}</Badge>
        <span v-else class="text-lighter">No disposition</span>
      </div>
      <div v-if="call.tags?.length" class="hstack" style="flex-wrap: wrap; gap: 0.25rem">
        <Badge v-for="t in call.tags" :key="t.id">{{ t.name }}</Badge>
      </div>
      <div v-if="call.notes" style="white-space: pre-wrap">{{ call.notes }}</div>
    </div>
  </div>
</template>
