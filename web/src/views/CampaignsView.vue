<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ArrowLeft, Ban, CheckCircle2, Circle, Download, FileAudio, Megaphone, MessageSquareText, Pause, PhoneOutgoing, Play, Plus, Rocket, Workflow } from "lucide-vue-next";
import { api } from "../api/client";
import type { Audio, Flow, Paginated } from "../api/types";
import { auth } from "../stores/auth";
import { usePoll } from "../composables/usePoll";
import { confirmDialog } from "../lib/confirm";
import { toast } from "../lib/toast";
import { campaignLabel, campaignTone, defaultCampaignConfig, recipientSummary, type Campaign, type CampaignAttempt, type CampaignConfig, type CampaignRecipient } from "../lib/campaigns";
import { Badge, Button, Card, DataTable, EmptyState, KpiCard, Pagination, Panel, type Column } from "../components/ui";

const listColumns: Column[] = [
  { key: "name", label: "Name" },
  { key: "type", label: "Type" },
  { key: "status", label: "Status" },
  { key: "scheduled_at", label: "Scheduled start" },
  { key: "created_at", label: "Created" },
];
const recipientColumns: Column[] = [
  { key: "phone", label: "Phone", mono: true },
  { key: "external_ref", label: "Reference" },
  { key: "status", label: "Status" },
  { key: "attempts", label: "Attempts", align: "right", mono: true },
];
const attemptColumns: Column[] = [
  { key: "phone", label: "Phone", mono: true },
  { key: "attempt", label: "Attempt", align: "right", mono: true },
  { key: "status", label: "Status" },
  { key: "answered_at", label: "Answered" },
  { key: "created_at", label: "Started" },
  { key: "call_id", label: "Call log" },
];
const typeOptions: { value: CampaignConfig["type"]; label: string; hint: string; icon: typeof FileAudio }[] = [
  { value: "audio", label: "Uploaded audio", hint: "Play a recorded message to every recipient.", icon: FileAudio },
  { value: "tts", label: "Text-to-speech", hint: "Type a message; Tring speaks it with the Piper voice.", icon: MessageSquareText },
  { value: "agent", label: "Agent-assisted", hint: "Ring a team agent first, then connect the client.", icon: PhoneOutgoing },
  { value: "ivr", label: "Interactive IVR", hint: "Run an IVR flow so recipients can respond with keypresses.", icon: Workflow },
];
const recipientStatuses = ["pending", "active", "completed", "incomplete", "busy", "no_answer", "failed", "unknown", "cancelled"];
// Oat has no "selected card" state; borrow the focus-ring colour so the choice reads like a focused control.
const selectedCard = { borderColor: "var(--primary)", boxShadow: "0 0 0 1px var(--primary)" };

const route = useRoute();
const router = useRouter();
const id = computed(() => typeof route.params.id === "string" ? route.params.id : "");
const current = ref<Campaign | null>(null);
const campaigns = ref<Campaign[]>([]);
const listPage = ref(1), listTotal = ref(0);
const recipients = ref<CampaignRecipient[]>([]), attempts = ref<CampaignAttempt[]>([]);
const recipientPage = ref(1), recipientTotal = ref(0), attemptPage = ref(1), attemptTotal = ref(0);
const statusFilter = ref("");
const name = ref("");
const config = ref(defaultCampaignConfig());
const sounds = ref<Audio[]>([]);
const flows = ref<Flow[]>([]);
const teams = ref<{ name: string }[]>([]);
const hooks = ref<{ id: string; name: string; active: boolean }[]>([]);
const numbers = ref<{ e164: string; active: boolean }[]>([]);
const busy = ref(false), loading = ref(false), error = ref(""), notice = ref("");
const tab = ref("setup");
const pasted = ref("");
const testNumber = ref("");
const startAt = ref("");
const scheduleMode = ref("now");
const importResult = ref<{ added: number; duplicates: number; rejected: { row: number; phone: string; error: string }[] } | null>(null);
const previewURLs = ref<Record<string, string>>({});
const draft = computed(() => !current.value || current.value.status === "draft");
// Disable each control rather than a wrapping fieldset: Oat dims every :disabled element to 50%
// opacity, so a disabled fieldset fades labels and headings too and locked settings become hard to read.
const locked = computed(() => busy.value || !draft.value);
const webhookSelection = computed({ get: () => config.value.webhook_id || "", set: (value: string) => { config.value.webhook_id = value || undefined; } });
const changed = computed(() => !!current.value && (name.value !== current.value.name || JSON.stringify(config.value) !== JSON.stringify(current.value.config)));
const summary = computed(() => recipientSummary(current.value?.counts || null));
const prepared = computed(() => !!current.value?.prepared && !changed.value);
const browserTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
const previews = computed(() => {
  const p = current.value?.prepared;
  if (!p || changed.value) return [];
  if (p.audio_file) return [{ label: "Message", path: p.audio_file }];
  return (p.flow?.nodes || []).filter(n => n.config?.audio_file).map(n => ({ label: n.label || n.id, path: n.config.audio_file! }));
});
// A launched campaign is about outcomes, so results lead; a draft follows the setup order.
const tabs = computed(() => {
  const r = { value: "recipients", label: `Recipients · ${summary.value.total}` };
  return draft.value
    ? [{ value: "setup", label: "Content & delivery" }, r, { value: "launch", label: "Review & launch" }, { value: "results", label: "Test calls" }]
    : [{ value: "results", label: "Results" }, r, { value: "setup", label: "Settings" }];
});
const readiness = computed(() => [
  { done: !!current.value && !changed.value, label: "Draft saved", hint: changed.value ? "You have unsaved changes." : "" },
  { done: prepared.value, label: config.value.type === "agent" ? "Team verified" : "Content prepared", hint: prepared.value ? "" : "Prepare the campaign in Content & delivery." },
  { done: summary.value.total > 0, label: "Recipients added", hint: summary.value.total ? `${summary.value.total} recipients` : "Import a CSV or paste numbers." },
  { done: !!current.value?.config.caller_id, label: "Caller ID selected", hint: current.value?.config.caller_id || "" },
]);
const ready = computed(() => readiness.value.every(r => r.done));
const finishedPct = computed(() => summary.value.total ? Math.round(summary.value.finished / summary.value.total * 100) : 0);

async function action(fn: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true; error.value = ""; notice.value = "";
  try { await fn(); } catch (e) { error.value = e instanceof Error ? e.message : "Request failed"; }
  finally { busy.value = false; }
}
function resetPreviews() { for (const url of Object.values(previewURLs.value)) URL.revokeObjectURL(url); previewURLs.value = {}; }
onBeforeUnmount(resetPreviews);

async function loadDetail(initial = false) {
  const target = id.value;
  if (!target || target === "new") return;
  const c = await api.get<Campaign>(`/v1/campaigns/${target}`);
  if (id.value !== target) return;
  const dirty = changed.value;
  // Keep the editor's revision when another session changes a dirty draft.
  // A later save must conflict rather than silently overwrite that session.
  if (!initial && dirty && current.value && current.value.revision !== c.revision) {
    current.value.counts = c.counts; current.value.metrics = c.metrics;
    notice.value = "This campaign changed in another session. Reload before saving your edits.";
    return;
  }
  current.value = c;
  if (initial || !dirty) { name.value = c.name; config.value = structuredClone(c.config); }
  if (initial) tab.value = c.status === "draft" ? "setup" : "results";
  if (tab.value === "recipients") await loadRecipients();
  if (tab.value === "results") await loadAttempts();
}
async function loadList() {
  const page = await api.get<Paginated<Campaign>>(`/v1/campaigns?page=${listPage.value}&limit=25`);
  campaigns.value = page.items; listTotal.value = page.total;
}
async function loadRecipients() {
  if (!current.value) return;
  const target = current.value.id;
  const page = await api.get<Paginated<CampaignRecipient>>(`/v1/campaigns/${target}/recipients?page=${recipientPage.value}&limit=25&status=${statusFilter.value}`);
  if (current.value?.id === target) { recipients.value = page.items; recipientTotal.value = page.total; }
}
async function loadAttempts() {
  if (!current.value) return;
  const target = current.value.id;
  const page = await api.get<Paginated<CampaignAttempt>>(`/v1/campaigns/${target}/attempts?page=${attemptPage.value}&limit=25`);
  if (current.value?.id === target) { attempts.value = page.items; attemptTotal.value = page.total; }
}
async function openRoute() {
  loading.value = true; error.value = ""; notice.value = ""; current.value = null; resetPreviews();
  recipientPage.value = 1; attemptPage.value = 1; recipients.value = []; attempts.value = []; importResult.value = null; pasted.value = "";
  try {
    if (!id.value) await loadList();
    else if (id.value === "new") { name.value = ""; config.value = defaultCampaignConfig(); tab.value = "setup"; }
    else await loadDetail(true);
  } catch (e) { error.value = e instanceof Error ? e.message : "Could not load campaigns"; }
  finally { loading.value = false; }
}
watch(id, openRoute, { immediate: true });
watch(listPage, () => action(loadList));
watch([recipientPage, statusFilter], () => action(loadRecipients));
watch(attemptPage, () => action(loadAttempts));
watch(tab, () => { if (tab.value === "recipients") void action(loadRecipients); if (tab.value === "results") void action(loadAttempts); });
watch(() => config.value.type, (type) => {
  if (type === "agent") {
    config.value.team ??= "";
    config.value.agent_wait_seconds ||= 120;
  }
});
watch(changed, (v) => { if (v) resetPreviews(); });
onMounted(async () => {
  try {
    const [a, f, n, h, t] = await Promise.all([api.get<Audio[]>("/v1/audio"), api.get<Paginated<Flow>>("/v1/flows"), api.get<{e164: string; active: boolean}[]>("/v1/numbers"), api.get<{id: string; name: string; active: boolean}[]>("/v1/webhooks"), api.get<{ name: string }[]>("/v1/teams")]);
    teams.value = t; hooks.value = h.filter(v => v.active); sounds.value = a; flows.value = f.items.filter(v => v.is_active); numbers.value = n.filter(v => v.active);
  } catch (e) { error.value = e instanceof Error ? e.message : "Could not load campaign options"; }
});
usePoll(() => {
  if (busy.value || loading.value) return;
  void (id.value ? loadDetail() : loadList()).catch(e => { error.value = e instanceof Error ? e.message : "Refresh failed"; });
}, 5000, { immediate: false });

async function save(announce = true) {
  if (current.value && !changed.value) return;
  const body = { name: name.value, config: config.value, revision: current.value?.revision || 0 };
  const c = current.value ? await api.put<Campaign>(`/v1/campaigns/${current.value.id}`, body) : await api.post<Campaign>("/v1/campaigns", body);
  current.value = c; config.value = structuredClone(c.config); name.value = c.name; resetPreviews();
  if (id.value === "new") { await router.replace(`/admin/campaigns/${c.id}`); await loadDetail(true); }
  if (announce) toast.success("Draft saved");
}
async function prepare() {
  await save(false);
  if (!current.value) return;
  current.value = await api.post<Campaign>(`/v1/campaigns/${current.value.id}/prepare`, { revision: current.value.revision });
  resetPreviews(); toast.success(config.value.type === "agent" ? "Team verified. You can place a test call." : "Content prepared. Preview it or place a test call.");
}
async function uploadAudio(event: Event) {
  const input = event.target as HTMLInputElement; const file = input.files?.[0]; if (!file) return;
  await action(async () => { const a = await api.upload<Audio>("/v1/audio", file, { name: `${file.name.replace(/\.[^.]+$/, "")} ${Date.now()}` }); sounds.value.unshift(a); config.value.audio_id = a.id; });
  input.value = "";
}
async function importCSV(event: Event) {
  const input = event.target as HTMLInputElement; const file = input.files?.[0]; if (!file || !current.value) return;
  await action(async () => { importResult.value = await api.upload(`/v1/campaigns/${current.value!.id}/recipients`, file); await loadDetail(); });
  input.value = "";
}
async function importPaste() {
  if (!current.value) return;
  const rows = pasted.value.split(/\r?\n/).map(phone => phone.trim()).filter(Boolean).map(phone => ({ phone, external_ref: "" }));
  importResult.value = await api.post(`/v1/campaigns/${current.value.id}/recipients`, { recipients: rows });
  pasted.value = ""; await loadDetail();
}
async function control(which: string) {
  if (!current.value) return;
  if (which === "cancel" && !(await confirmDialog("Cancel all remaining recipients? Calls already in progress will finish.", { title: "Cancel campaign", confirmLabel: "Cancel remaining calls" }))) return;
  let scheduled_at: string | undefined;
  if (which === "schedule") {
    const date = new Date(startAt.value);
    if (!Number.isFinite(date.getTime()) || date.getTime() <= Date.now()) throw new Error("Choose a future start time.");
    scheduled_at = date.toISOString();
  }
  current.value = await api.post<Campaign>(`/v1/campaigns/${current.value.id}/${which}`, { revision: current.value.revision, scheduled_at });
  tab.value = "results"; await loadAttempts(); toast.success(`Campaign ${campaignLabel(current.value.status).toLowerCase()}`);
}
async function testCall() {
  if (!current.value) return;
  await api.post(`/v1/campaigns/${current.value.id}/test`, { to: testNumber.value, revision: current.value.revision });
  await loadDetail(); tab.value = "results"; await loadAttempts(); toast.success("Test call queued. Its outcome will appear under Test calls.");
}
async function download(path: string) {
  const token = auth.token(); const res = await fetch(path, { headers: token ? { Authorization: `Bearer ${token}` } : {} });
  if (!res.ok) throw new Error("Download failed"); return URL.createObjectURL(await res.blob());
}
async function preview(path: string) { if (!previewURLs.value[path]) previewURLs.value[path] = await download(`/v1/audio/preview?path=${encodeURIComponent(path)}`); }
async function exportResults() {
  if (!current.value) return;
  const url = await download(`/v1/campaigns/${current.value.id}/export`); const a = document.createElement("a"); a.href = url; a.download = `campaign-${current.value.id}.csv`; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
}
const dateLabel = (value: string | null) => value ? new Date(value).toLocaleString() : "—";
</script>

<template>
  <div class="view">
    <!-- Header -->
    <div class="row-between">
      <div class="vstack gap-1">
        <RouterLink v-if="id" to="/admin/campaigns" class="unstyled hstack gap-1 text-light"><ArrowLeft :size="14" /><small>All campaigns</small></RouterLink>
        <div class="hstack gap-2">
          <h1 class="page-title">{{ id ? (current?.name || "New campaign") : "Campaigns" }}</h1>
          <Badge v-if="current" :tone="campaignTone(current.status)">{{ campaignLabel(current.status) }}</Badge>
        </div>
        <p class="text-light">{{ current ? campaignLabel(current.config.type) : "Voice blasts, IVR and agent-assisted calls." }}</p>
      </div>
      <div class="hstack">
        <RouterLink v-if="!id" to="/admin/campaigns/new" class="button"><Plus :size="16" />New campaign</RouterLink>
        <template v-if="current">
          <Button v-if="['running', 'scheduled'].includes(current.status)" variant="secondary" :disabled="busy" @click="action(() => control('pause'))"><Pause :size="16" />Pause</Button>
          <Button v-if="current.status === 'paused'" :disabled="busy" @click="action(() => control('resume'))"><Play :size="16" />Resume</Button>
          <button v-if="['draft', 'running', 'scheduled', 'paused'].includes(current.status)" type="button" class="outline" data-variant="danger" :disabled="busy" @click="action(() => control('cancel'))"><Ban :size="16" />Cancel campaign</button>
        </template>
      </div>
    </div>

    <div v-if="error" role="alert" data-variant="danger">{{ error }}</div>
    <div v-if="notice" role="alert" data-variant="warning">{{ notice }}</div>
    <p v-if="loading" class="text-light" aria-live="polite">Loading…</p>

    <!-- List -->
    <template v-else-if="!id">
      <Card v-if="!campaigns.length" :padded="false">
        <EmptyState :icon="Megaphone" title="Create your first campaign" hint="Upload a message or turn text into speech, add recipients, then test and schedule your calls.">
          <template #action><RouterLink to="/admin/campaigns/new" class="button"><Plus :size="16" />New campaign</RouterLink></template>
        </EmptyState>
      </Card>
      <Panel v-else>
        <DataTable :columns="listColumns" :rows="campaigns">
          <template #cell-name="{ row }"><RouterLink :to="`/admin/campaigns/${row.id}`" class="unstyled">{{ row.name }}</RouterLink></template>
          <template #cell-type="{ row }"><span class="text-light">{{ campaignLabel(row.config.type) }}</span></template>
          <template #cell-status="{ row }"><Badge :tone="campaignTone(row.status)">{{ campaignLabel(row.status) }}</Badge></template>
          <template #cell-scheduled_at="{ row }">{{ dateLabel(row.scheduled_at) }}</template>
          <template #cell-created_at="{ row }"><span class="text-light">{{ dateLabel(row.created_at) }}</span></template>
        </DataTable>
      </Panel>
      <Pagination v-model:page="listPage" :page-size="25" :total="listTotal" label="campaigns" />
    </template>

    <!-- Detail / editor -->
    <template v-else-if="current || id === 'new'">
      <div v-if="current && !draft" class="row">
        <KpiCard class="col-3" :value="summary.total" label="Recipients" :sub="`${summary.pending} pending`" />
        <KpiCard class="col-3" :value="summary.active" label="Calling now" />
        <KpiCard class="col-3" :value="`${finishedPct}%`" label="Finished">
          <progress :value="summary.finished" :max="summary.total || 1" aria-label="Recipients finished" />
          <small class="text-lighter">{{ summary.finished }} of {{ summary.total }}</small>
        </KpiCard>
        <KpiCard class="col-3" :value="summary.completed" label="Completed" :tone="summary.completed ? 'success' : undefined" :sub="`${current.metrics?.answered || 0} answered · ${current.metrics?.attempts || 0} attempts`" />
      </div>

      <!-- Same small filled/outline button group as the live-calls filter on the dashboard. Oat's
           outline button is transparent (it reads white there because it sits on a panel), so each
           gets a card-coloured backing here on the grey page; hover/selected still come from Oat. -->
      <span v-if="current" class="hstack gap-1" role="group" aria-label="Campaign sections">
        <span v-for="t in tabs" :key="t.value" style="background: var(--card); border-radius: var(--radius-medium)">
          <button type="button" class="small" :class="{ outline: tab !== t.value }" :aria-pressed="tab === t.value" @click="tab = t.value">{{ t.label }}</button>
        </span>
      </span>
      <p v-else class="text-light">Start with the content and delivery settings. Save your draft to unlock recipients, review and launch.</p>

      <!-- Content & delivery -->
      <form v-if="tab === 'setup'" class="row" @submit.prevent="action(() => save())">
        <div class="col-8 vstack gap-4">
          <div v-if="current && !draft" role="alert">Settings are locked once a campaign is scheduled, started or cancelled. Create a new campaign to change them.</div>
            <Panel pad>
              <template #title>Content</template>
              <div data-field><label for="c-name">Name<span class="req">*</span></label><input id="c-name" v-model="name" required maxlength="200" placeholder="Appointment reminders" :disabled="locked" /></div>
              <div data-field>
                <label>Campaign type</label>
                <div class="grid grid-2 mt-2" role="radiogroup" aria-label="Campaign type">
                  <label v-for="t in typeOptions" :key="t.value" class="card p-4" style="align-items: flex-start" :style="config.type === t.value ? selectedCard : undefined">
                    <input v-model="config.type" type="radio" name="campaign-type" :value="t.value" :disabled="locked" style="flex-shrink: 0" />
                    <span class="vstack gap-1">
                      <strong class="hstack gap-2"><component :is="t.icon" :size="16" />{{ t.label }}</strong>
                      <small class="text-light">{{ t.hint }}</small>
                    </span>
                  </label>
                </div>
              </div>
              <div v-if="config.type === 'audio'" class="grid grid-2">
                <div data-field>
                  <label for="c-sound">Sound<span class="req">*</span></label>
                  <select id="c-sound" v-model="config.audio_id" required :disabled="locked"><option value="" disabled>Select a sound</option><option v-for="sound in sounds" :key="sound.id" :value="sound.id">{{ sound.name }}</option></select>
                </div>
                <div data-field>
                  <label for="c-upload">Or upload new audio</label>
                  <input id="c-upload" type="file" accept=".wav,.mp3,.ogg,.m4a" @change="uploadAudio" :disabled="locked" />
                  <small data-hint>WAV, MP3, OGG or M4A, up to 10 MB.</small>
                </div>
              </div>
              <div v-if="config.type === 'tts'" data-field>
                <label for="c-text">Message<span class="req">*</span></label>
                <textarea id="c-text" v-model="config.text" required rows="5" maxlength="5000" placeholder="Hello, this is a reminder about your appointment tomorrow." :disabled="locked" />
                <small data-hint>{{ (config.text || "").length }} / 5000 · The same message plays for every recipient.</small>
              </div>
              <div v-if="config.type === 'ivr'" data-field>
                <label for="c-flow">IVR flow<span class="req">*</span></label>
                <select id="c-flow" v-model="config.flow_id" required :disabled="locked"><option value="" disabled>Select a flow</option><option v-for="flow in flows" :key="flow.id" :value="flow.id">{{ flow.name }}</option></select>
              </div>
              <div v-if="config.type === 'agent'" class="grid grid-2">
                <div data-field>
                  <label for="c-team">Team<span class="req">*</span></label>
                  <select id="c-team" v-model="config.team" required :disabled="locked"><option value="" disabled>Select a team</option><option v-for="team in teams" :key="team.name" :value="team.name">{{ team.name }}</option></select>
                  <small data-hint>Available agents answer in their Tring app. The client is dialed only after an agent accepts.</small>
                  <small v-if="!teams.length" data-hint>Create a team and add agents first.</small>
                </div>
                <div data-field>
                  <label for="c-agent-wait">Maximum agent wait (s)</label>
                  <input id="c-agent-wait" v-model.number="config.agent_wait_seconds" type="number" min="5" max="3600" required :disabled="locked" />
                  <small data-hint>Uses the team's ringing strategy and queue timeout, capped by this wait. No agent answer uses one attempt; retries follow your settings.</small>
                </div>
              </div>
              <div data-field>
                <label for="c-caller">Caller ID<span class="req">*</span></label>
                <select id="c-caller" v-model="config.caller_id" required :disabled="locked"><option value="" disabled>Select an active number</option><option v-for="n in numbers" :key="n.e164" :value="n.e164">{{ n.e164 }}</option></select>
              </div>
              <div v-if="!numbers.length" role="alert" data-variant="warning">Add an active number and outbound trunk before launching a campaign.</div>
            </Panel>

            <Panel pad>
              <template #title>Calling window</template>
              <div class="grid grid-3">
                <div data-field><label for="c-tz">Timezone<span class="req">*</span></label><input id="c-tz" v-model="config.timezone" required placeholder="Asia/Kolkata" :disabled="locked" /></div>
                <div data-field><label for="c-from">Call from<span class="req">*</span></label><input id="c-from" v-model="config.window_start" type="time" required :disabled="locked" /></div>
                <div data-field><label for="c-until">Until<span class="req">*</span></label><input id="c-until" v-model="config.window_end" type="time" required :disabled="locked" /></div>
              </div>
              <small class="text-light">New calls start only inside this daily window. End time must be later than start time.</small>
            </Panel>

            <Panel pad>
              <template #title>Pacing &amp; retries</template>
              <div class="grid grid-3">
                <div data-field><label for="c-conc">Concurrent calls</label><input id="c-conc" v-model.number="config.concurrency" type="number" min="1" max="100" required :disabled="locked" /></div>
                <div data-field><label for="c-cpm">Calls per minute</label><input id="c-cpm" v-model.number="config.calls_per_minute" type="number" min="1" max="300" required :disabled="locked" /></div>
                <div data-field><label for="c-ring">Ring timeout (s)</label><input id="c-ring" v-model.number="config.ring_seconds" type="number" min="5" max="120" required :disabled="locked" /></div>
                <div data-field><label for="c-max">Max call length (s)</label><input id="c-max" v-model.number="config.max_seconds" type="number" min="10" max="3600" required :disabled="locked" /></div>
                <div data-field><label for="c-att">Attempts per recipient</label><input id="c-att" v-model.number="config.max_attempts" type="number" min="1" max="5" required :disabled="locked" /></div>
                <div data-field><label for="c-retry">Retry after (min)</label><input id="c-retry" v-model.number="config.retry_minutes" type="number" min="1" max="10080" required :disabled="locked || config.max_attempts <= 1" /></div>
              </div>
              <small class="text-light">Retries apply to busy, unanswered and temporary carrier failures. Shared system limits may reduce the dialing rate.</small>
              <div data-field class="mt-4">
                <label for="c-hook">Result webhook</label>
                <select id="c-hook" v-model="webhookSelection" :disabled="locked"><option value="">None</option><option v-for="hook in hooks" :key="hook.id" :value="hook.id">{{ hook.name }}</option></select>
                <small data-hint>Optional. Receives each recipient's final outcome.</small>
              </div>
            </Panel>
        </div>

        <div class="col-4">
          <div style="position: sticky; top: 1rem">
            <Panel pad>
              <template #title>{{ config.type === "agent" ? "Agent connection" : "Preview" }}</template>
              <template #actions>
                <Badge v-if="changed" tone="warning">Unsaved</Badge>
                <Badge v-else-if="prepared" tone="success">Prepared</Badge>
                <Badge v-else-if="current" tone="neutral">Not prepared</Badge>
              </template>
              <div class="vstack">
                <template v-if="draft">
                  <Button block :loading="busy" @click="action(prepare)">{{ config.type === "agent" ? "Verify team & prepare" : "Prepare audio & preview" }}</Button>
                  <Button block type="submit" variant="secondary" :disabled="busy">Save draft</Button>
                  <small class="text-light">{{ changed ? "Prepare again after changes, before testing or launching." : config.type === "agent" ? "Verify the team before testing or launching. No audio message is required." : "Preparing renders the audio each recipient will hear." }}</small>
                </template>
                <small v-else class="text-light">Content is frozen for this campaign.</small>
                <div v-for="p in previews" :key="p.path" class="vstack gap-2">
                  <div class="hstack justify-between"><strong class="truncate">{{ p.label }}</strong><Button v-if="!previewURLs[p.path]" size="sm" variant="secondary" :disabled="busy" @click="action(() => preview(p.path))"><Play :size="14" />Load</Button></div>
                  <audio v-if="previewURLs[p.path]" :src="previewURLs[p.path]" controls preload="metadata" style="width: 100%" />
                </div>
              </div>
            </Panel>
          </div>
        </div>
      </form>

      <!-- Recipients -->
      <div v-if="tab === 'recipients' && current" class="row">
        <div v-if="draft" class="col-4">
          <Panel pad>
            <template #title>Add recipients</template>
            <div data-field>
              <label for="r-csv">Upload CSV</label>
              <input id="r-csv" type="file" accept=".csv,text/csv" :disabled="busy" @change="importCSV" />
              <small data-hint>Columns <code>phone</code> and optional <code>external_ref</code>. Up to 10,000 rows / 2 MB. Existing numbers are skipped.</small>
            </div>
            <div data-field>
              <label for="r-paste">Or paste phone numbers</label>
              <textarea id="r-paste" v-model="pasted" class="mono" rows="6" placeholder="One phone number per line" :disabled="busy" />
              <small data-hint>Validated as Indian mobile numbers.</small>
            </div>
            <Button block :disabled="busy || !pasted.trim()" @click="action(importPaste)"><Plus :size="16" />Add recipients</Button>
            <div v-if="importResult" class="vstack gap-2 mt-4">
              <div role="alert" :data-variant="importResult.rejected.length ? 'warning' : 'success'">{{ importResult.added }} added · {{ importResult.duplicates }} duplicates · {{ importResult.rejected.length }} rejected</div>
              <details v-if="importResult.rejected.length"><summary>Rejected rows</summary><ul><li v-for="row in importResult.rejected" :key="row.row"><small>Row {{ row.row }} — <span class="mono">{{ row.phone }}</span>: {{ row.error }}</small></li></ul></details>
            </div>
          </Panel>
        </div>
        <div :class="draft ? 'col-8 vstack' : 'col-12 vstack'">
          <Panel>
            <template #title>Recipients</template>
            <template #actions>
              <select v-model="statusFilter" aria-label="Filter by outcome" @change="recipientPage = 1"><option value="">All outcomes</option><option v-for="s in recipientStatuses" :key="s" :value="s">{{ campaignLabel(s) }}</option></select>
            </template>
            <DataTable :columns="recipientColumns" :rows="recipients" :empty-text="statusFilter ? 'No recipients match this filter.' : 'No recipients yet.'">
              <template #cell-external_ref="{ row }"><span class="text-light">{{ row.external_ref || "—" }}</span></template>
              <template #cell-status="{ row }"><Badge :tone="campaignTone(row.status)">{{ campaignLabel(row.status) }}</Badge></template>
            </DataTable>
          </Panel>
          <Pagination v-model:page="recipientPage" :page-size="25" :total="recipientTotal" label="recipients" />
        </div>
      </div>

      <!-- Review & launch -->
      <div v-if="tab === 'launch' && current" class="row">
        <div class="col-7 vstack gap-4">
          <Panel pad>
            <template #title>Readiness</template>
            <ul class="unstyled vstack" style="margin: 0">
              <li v-for="r in readiness" :key="r.label" class="hstack gap-2">
                <CheckCircle2 v-if="r.done" :size="18" style="color: var(--success)" />
                <Circle v-else :size="18" class="text-lighter" />
                <span>{{ r.label }}</span>
                <small v-if="r.hint" class="text-light">· {{ r.hint }}</small>
              </li>
            </ul>
          </Panel>
          <Panel>
            <template #title>Summary</template>
            <table>
              <tbody>
                <tr><th scope="row">Type</th><td>{{ campaignLabel(current.config.type) }}</td></tr>
                <tr v-if="current.config.type === 'agent'"><th scope="row">Team</th><td>{{ current.config.team }}</td></tr>
                <tr><th scope="row">Recipients</th><td class="mono">{{ summary.total }}</td></tr>
                <tr><th scope="row">Caller ID</th><td class="mono">{{ current.config.caller_id || "—" }}</td></tr>
                <tr><th scope="row">Calling hours</th><td>{{ current.config.window_start }}–{{ current.config.window_end }} <span class="text-light">({{ current.config.timezone }})</span></td></tr>
                <tr><th scope="row">Pacing</th><td>Up to {{ current.config.concurrency }} concurrent, {{ current.config.calls_per_minute }} per minute</td></tr>
                <tr><th scope="row">Attempts</th><td>{{ current.config.max_attempts }} per recipient<span v-if="current.config.max_attempts > 1" class="text-light">, retry after {{ current.config.retry_minutes }} min</span></td></tr>
              </tbody>
            </table>
          </Panel>
        </div>
        <div class="col-5 vstack gap-4">
          <Panel pad>
            <template #title>Test call</template>
            <div data-field>
              <label for="l-test">Phone number</label>
              <input id="l-test" v-model="testNumber" type="tel" class="mono" placeholder="+91…" :disabled="busy" />
              <small v-if="config.type === 'agent'" data-hint>The selected team rings first. Once an agent accepts, this test number is dialed.</small>
              <small data-hint>Places a real call now, outside the schedule. Tests don't change recipient results.</small>
            </div>
            <Button block variant="secondary" :disabled="busy || !prepared || !testNumber" @click="action(testCall)"><PhoneOutgoing :size="16" />Place test call</Button>
          </Panel>
          <Panel pad>
            <template #title>Launch</template>
            <div class="vstack">
              <div class="hstack" role="radiogroup" aria-label="Start">
                <label><input v-model="scheduleMode" type="radio" name="schedule-mode" value="now" :disabled="busy" />Start now</label>
                <label><input v-model="scheduleMode" type="radio" name="schedule-mode" value="later" :disabled="busy" />Schedule for later</label>
              </div>
              <div v-if="scheduleMode === 'later'" data-field>
                <label for="l-start">Start time <span class="text-light">({{ browserTimezone }})</span></label>
                <input id="l-start" v-model="startAt" type="datetime-local" :disabled="busy" />
              </div>
              <small class="text-light">New calls begin only within the daily calling hours.</small>
              <Button block :disabled="busy || !ready" @click="action(() => control(scheduleMode === 'later' ? 'schedule' : 'start'))"><Rocket :size="16" />{{ scheduleMode === "later" ? "Schedule campaign" : "Start campaign" }}</Button>
              <small v-if="!ready" class="text-lighter">Complete the readiness checklist to launch.</small>
            </div>
          </Panel>
        </div>
      </div>

      <!-- Results / test calls -->
      <div v-if="tab === 'results' && current" class="vstack gap-4">
        <div v-if="current.counts?.unknown" role="alert" data-variant="warning">Some attempts need review after a gateway interruption. They will not be retried automatically.</div>
        <Panel>
          <template #title>{{ draft ? "Test calls" : "Attempt history" }}</template>
          <template #actions>
            <Button v-if="!draft" size="sm" variant="secondary" :disabled="busy" @click="action(exportResults)"><Download :size="14" />Export results</Button>
          </template>
          <DataTable :columns="attemptColumns" :rows="attempts" :empty-text="draft ? 'No test calls yet. Place one from Review & launch.' : 'No calls yet.'">
            <template #cell-phone="{ row }"><span class="mono">{{ row.phone }}</span> <Badge v-if="!row.recipient_id" tone="accent">Test</Badge></template>
            <template #cell-status="{ row }">
              <Badge :tone="campaignTone(row.status)">{{ campaignLabel(row.status) }}</Badge>
              <small v-if="row.error" class="text-light"> {{ row.error.replaceAll("_", " ") }}</small>
            </template>
            <template #cell-answered_at="{ row }">{{ dateLabel(row.answered_at) }}</template>
            <template #cell-created_at="{ row }"><span class="text-light">{{ dateLabel(row.created_at) }}</span></template>
            <template #cell-call_id="{ row }"><RouterLink v-if="row.call_id" :to="`/admin/calls/${row.call_id}`">View call</RouterLink><span v-else class="text-lighter">—</span></template>
          </DataTable>
        </Panel>
        <Pagination v-model:page="attemptPage" :page-size="25" :total="attemptTotal" label="attempts" />
        <small v-if="!draft" class="text-lighter">{{ current.config.type === "agent" ? "Completed means the client connected to an agent and the conversation ended." : current.config.type === "ivr" ? "Completed means the IVR flow finished." : "Completed means the entire message finished playing." }} Metrics exclude test calls.</small>
      </div>
    </template>
  </div>
</template>
