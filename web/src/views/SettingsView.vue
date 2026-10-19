<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
import { Building2, PhoneCall, Mic, Route, Globe } from "lucide-vue-next";
import { errMessage } from "../lib/errors";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import AudioPicker from "../components/AudioPicker.vue";
import { Card, Button, Select, Toggle, Badge, CopyButton } from "../components/ui";
import type { Tenant, GuestConfig, Flow, Paginated } from "../api/types";

const tenant = ref<Tenant | null>(null);
const guest = ref<GuestConfig | null>(null);
const flows = ref<Flow[]>([]);
const saving = ref(false);

// The editable form. Saved values are snapshotted after load/save; the save
// bar shows while the form differs from them.
const form = ref({
  strategy: "failover",
  holdMusic: "", // an uploaded sound's file_path; "" = built-in default
  recordCalls: true,
  recordingFormat: "ogg",
  recordingSampleRate: "8000", // a string because <Select> emits one
  maxCallMinutes: 240, // stored as seconds on the tenant
  transferFailedMessage: "",
  defaultFlowId: "", // guest web calling
});
const saved = ref("");
const snapshot = () => JSON.stringify(form.value);
const dirty = computed(() => saved.value !== "" && snapshot() !== saved.value);
const guestDirty = () => form.value.defaultFlowId !== JSON.parse(saved.value).defaultFlowId;

// Mirrors internal/trunkselect. load_based is reserved there (outbound calls
// fail with it), so it's listed but can't be picked.
const strategies = [
  { value: "failover", title: "Failover", desc: "Try trunks in priority order until one connects." },
  { value: "round_robin", title: "Round robin", desc: "Rotate calls across trunks, one trunk per call." },
  { value: "manual", title: "Manual", desc: "Always use the first trunk by priority, no fallback." },
  { value: "load_based", title: "Load based", desc: "Not available yet.", disabled: true },
];

const formats = [
  { value: "ogg", title: "OGG", desc: "Smallest files. Plays in modern browsers." },
  { value: "mp3", title: "MP3", desc: "Plays on any device or tool." },
  { value: "wav", title: "WAV", desc: "Uncompressed. For speech analytics." },
];

const sampleRateOptions = [
  { value: "8000", label: "8 kHz · phone quality" },
  { value: "12000", label: "12 kHz" },
  { value: "16000", label: "16 kHz · wideband" },
  { value: "24000", label: "24 kHz" },
  { value: "48000", label: "48 kHz · full quality" },
];

// Approximate stereo bitrate (kbps) of the finished recording, matching the
// gateway's encoder settings (internal/media/record/merge.go).
const kbps: Record<string, Record<string, number>> = {
  ogg: { 8000: 24, 12000: 32, 16000: 40, 24000: 64, 48000: 96 },
  mp3: { 8000: 24, 12000: 32, 16000: 48, 24000: 64, 48000: 128 },
};
const sizePerMinute = computed(() => {
  const rate = Number(form.value.recordingSampleRate);
  const k = form.value.recordingFormat === "wav" ? (rate * 32) / 1000 : kbps[form.value.recordingFormat][rate];
  const kb = (k * 60) / 8;
  return kb >= 1000 ? `${(kb / 1000).toFixed(1)} MB` : `${Math.round(kb)} KB`;
});

const flowOptions = computed(() => [
  { value: "", label: "None (your backend passes flow_id)" },
  ...flows.value.filter((f) => f.is_active).map((f) => ({ value: f.id, label: f.name })),
]);

function fromTenant(t: Tenant) {
  Object.assign(form.value, {
    strategy: t.outbound_strategy || "failover",
    holdMusic: t.hold_music || "",
    recordCalls: t.record_calls ?? true,
    recordingFormat: t.recording_format || "ogg",
    recordingSampleRate: String(t.recording_sample_rate ?? 8000),
    maxCallMinutes: Math.round((t.max_call_seconds ?? 14400) / 60),
    transferFailedMessage: t.transfer_failed_message || "",
  });
}

async function load() {
  try {
    const t = await api.get<Tenant>("/v1/tenants/me");
    tenant.value = t;
    fromTenant(t);
  } catch (e) {
    toast.error(errMessage(e, "failed to load settings"));
    return;
  }
  // Guest web calling is its own endpoint; the rest of the page works without it.
  try {
    const [g, fl] = await Promise.all([
      api.get<GuestConfig>("/v1/guest-config"),
      api.get<Paginated<Flow>>("/v1/flows"),
    ]);
    guest.value = g;
    flows.value = fl.items;
    form.value.defaultFlowId = g.default_flow_id || "";
  } catch (e) {
    toast.error(errMessage(e, "failed to load guest config"));
  }
  saved.value = snapshot();
}

async function save() {
  saving.value = true;
  try {
    const f = form.value;
    tenant.value = await api.patch<Tenant>("/v1/tenants/me", {
      outbound_strategy: f.strategy,
      hold_music: f.holdMusic,
      record_calls: f.recordCalls,
      recording_format: f.recordingFormat,
      recording_sample_rate: Number(f.recordingSampleRate),
      max_call_seconds: Math.round(f.maxCallMinutes * 60),
      transfer_failed_message: f.transferFailedMessage,
    });
    fromTenant(tenant.value);
    if (guest.value && guestDirty()) {
      guest.value = await api.patch<GuestConfig>("/v1/guest-config", { default_flow_id: f.defaultFlowId });
      f.defaultFlowId = guest.value.default_flow_id || "";
    }
    saved.value = snapshot();
    toast.success("Settings saved");
  } catch (e) {
    toast.error(errMessage(e, "save failed"));
  } finally {
    saving.value = false;
  }
}

function discard() {
  form.value = JSON.parse(saved.value);
}

onMounted(load);
</script>

<template>
  <div class="view settings">
    <div>
      <h1 class="page-title">Settings</h1>
      <p class="text-light">Workspace-wide defaults for calls, recording and routing.</p>
    </div>

    <template v-if="tenant">
      <!-- Workspace -->
      <Card class="section">
        <header>
          <span class="section-icon"><Building2 :size="16" /></span>
          <div>
            <h2>Workspace</h2>
            <p>Your tenant's identity. Set when the tenant was created.</p>
          </div>
        </header>
        <div class="srow">
          <div class="srow-label">Name</div>
          <div class="srow-value">{{ tenant.name }}</div>
        </div>
        <div class="srow">
          <div class="srow-label">SIP domain</div>
          <div class="srow-value hstack">
            <span class="mono">{{ tenant.sip_domain }}</span>
            <CopyButton :value="tenant.sip_domain" />
          </div>
        </div>
      </Card>

      <!-- Calls -->
      <Card class="section">
        <header>
          <span class="section-icon"><PhoneCall :size="16" /></span>
          <div>
            <h2>Calls</h2>
            <p>What callers hear, and how long a call may last.</p>
          </div>
        </header>
        <div class="srow">
          <div>
            <label class="srow-label" for="max-call-minutes">Max call duration</label>
            <p class="srow-desc">
              Calls end after this. Also how long browser relay (TURN) credentials stay valid. 1–1440 minutes.
            </p>
          </div>
          <div class="hstack">
            <input id="max-call-minutes" v-model.number="form.maxCallMinutes" type="number" min="1" max="1440" class="num" />
            <span class="text-light">minutes</span>
          </div>
        </div>
        <div class="srow">
          <div>
            <div class="srow-label">Hold music</div>
            <p class="srow-desc">Played to callers waiting in a team's queue. Upload audio here or under Sounds.</p>
          </div>
          <AudioPicker v-model="form.holdMusic" default-label="Default (built-in music)" />
        </div>
        <div class="srow">
          <div>
            <label class="srow-label" for="transfer-failed-message">No-answer message</label>
            <p class="srow-desc">Spoken to the caller when a transfer finds no one available.</p>
          </div>
          <textarea id="transfer-failed-message" v-model="form.transferFailedMessage" rows="3"></textarea>
        </div>
      </Card>

      <!-- Recording -->
      <Card class="section">
        <header>
          <span class="section-icon"><Mic :size="16" /></span>
          <div>
            <h2>Recording</h2>
            <p>Stereo recordings: agent on the left channel, caller on the right.</p>
          </div>
        </header>
        <div class="srow">
          <div>
            <div class="srow-label">Record calls</div>
            <p class="srow-desc">
              From when an agent answers. Recordings appear in the Calls log. Make sure recording is
              permitted and disclosed where you operate.
            </p>
          </div>
          <Toggle v-model="form.recordCalls" :label="form.recordCalls ? 'On' : 'Off'" />
        </div>
        <div class="srow stacked" :class="{ off: !form.recordCalls }">
          <div>
            <div class="srow-label">Format</div>
            <p class="srow-desc">File type of the saved recording.</p>
          </div>
          <div class="choices choices-3" role="radiogroup" aria-label="Recording format">
            <label v-for="f in formats" :key="f.value" class="choice" :class="{ on: form.recordingFormat === f.value }">
              <input v-model="form.recordingFormat" type="radio" name="recording-format" :value="f.value" :disabled="!form.recordCalls" />
              <span class="choice-title">{{ f.title }}</span>
              <span class="choice-desc">{{ f.desc }}</span>
            </label>
          </div>
        </div>
        <div class="srow" :class="{ off: !form.recordCalls }">
          <div>
            <div class="srow-label">Quality</div>
            <p class="srow-desc">Phone calls carry 8 kHz audio; higher rates only help browser and WhatsApp calls.</p>
          </div>
          <div>
            <Select v-model="form.recordingSampleRate" :options="sampleRateOptions" :disabled="!form.recordCalls" />
            <span class="estimate">About {{ sizePerMinute }} per minute of call</span>
          </div>
        </div>
        <p class="section-note">Changes apply to calls that start after you save.</p>
      </Card>

      <!-- Outbound routing -->
      <Card class="section">
        <header>
          <span class="section-icon"><Route :size="16" /></span>
          <div>
            <h2>Outbound routing</h2>
            <p>How outgoing calls pick a trunk when you have more than one.</p>
          </div>
        </header>
        <div class="srow stacked">
          <div>
            <div class="srow-label">Trunk strategy</div>
          </div>
          <div class="choices choices-4" role="radiogroup" aria-label="Outbound trunk strategy">
            <label
              v-for="s in strategies"
              :key="s.value"
              class="choice"
              :class="{ on: form.strategy === s.value, disabled: s.disabled && form.strategy !== s.value }"
            >
              <input v-model="form.strategy" type="radio" name="strategy" :value="s.value" :disabled="s.disabled" />
              <span class="choice-title">{{ s.title }}</span>
              <span class="choice-desc">{{ s.desc }}</span>
            </label>
          </div>
        </div>
        <div v-if="tenant.failover_causes?.length" class="srow">
          <div>
            <div class="srow-label">Fails over on</div>
            <p class="srow-desc">Hangup causes that move a call to the next trunk.</p>
          </div>
          <div class="chip-wrap">
            <Badge v-for="c in tenant.failover_causes" :key="c" tone="neutral">{{ c }}</Badge>
          </div>
        </div>
      </Card>

      <!-- Guest web calling -->
      <Card v-if="guest" class="section">
        <header>
          <span class="section-icon"><Globe :size="16" /></span>
          <div>
            <h2>Guest web calling</h2>
            <p>
              Website visitors calling from the browser widget. Visitor tokens come from
              <code>POST /v1/guest-credentials</code>, called by your backend with an API key.
            </p>
          </div>
        </header>
        <div class="srow">
          <div>
            <div class="srow-label">Default flow</div>
            <p class="srow-desc">The IVR flow a guest call lands in when your backend doesn't pass a flow_id. Only active flows are listed.</p>
          </div>
          <Select v-model="form.defaultFlowId" :options="flowOptions" />
        </div>
      </Card>

      <Transition name="savebar">
        <div v-if="dirty" class="savebar" role="status">
          <span>You have unsaved changes</span>
          <div class="hstack">
            <Button variant="ghost" :disabled="saving" @click="discard">Discard</Button>
            <Button :loading="saving" @click="save">Save changes</Button>
          </div>
        </div>
      </Transition>
    </template>
  </div>
</template>

<style scoped>
.settings {
  width: 100%;
  max-width: 56rem;
  margin-inline: auto;
  padding-bottom: 5rem; /* room for the save bar */
}

.section {
  padding: 0;
  overflow: hidden;
}
.section header {
  display: flex;
  gap: 0.875rem;
  align-items: flex-start;
  padding: 1.25rem 1.5rem;
  border-bottom: 1px solid var(--border);
}
.section h2 {
  font-size: var(--text-6);
  font-weight: 600;
  margin: 0;
}
.section header p {
  margin: 0.125rem 0 0;
  font-size: var(--text-7);
  color: var(--muted-foreground);
}
.section-icon {
  display: grid;
  place-items: center;
  flex: none;
  width: 2rem;
  height: 2rem;
  border-radius: var(--radius-medium);
  background: var(--color-accent-soft);
  color: var(--primary);
}

/* One setting: label + description on the left, the control on the right. */
.srow {
  display: grid;
  grid-template-columns: minmax(0, 16rem) minmax(0, 1fr);
  gap: 0.5rem 2rem;
  align-items: start;
  padding: 1.125rem 1.5rem;
}
.srow.stacked {
  /* Choice cards need the width: label above, cards below. */
  grid-template-columns: minmax(0, 1fr);
  gap: 0.75rem;
}
.srow + .srow {
  border-top: 1px solid var(--border);
}
.srow-label {
  display: block;
  font-size: var(--text-7);
  font-weight: 500;
  margin: 0;
}
.srow-desc {
  margin: 0.25rem 0 0;
  font-size: var(--text-8);
  line-height: 1.5;
  color: var(--muted-foreground);
}
.srow-value {
  font-size: var(--text-7);
  min-height: 1.75rem;
  align-items: center;
}
.srow.off {
  opacity: 0.45;
  pointer-events: none;
}
/* The Select / textarea carry Oat's field spacing; rows already space them. */
.srow :deep([data-field]) {
  margin: 0;
}
.num {
  width: 7rem;
}
textarea {
  width: 100%;
}

.estimate {
  display: block;
  margin-top: 0.5rem;
  font-size: var(--text-8);
  color: var(--muted-foreground);
}
.section-note {
  margin: 0;
  padding: 0.75rem 1.5rem;
  font-size: var(--text-8);
  color: var(--muted-foreground);
  background: var(--secondary);
  border-top: 1px solid var(--border);
}

/* Radio cards: strategy and recording format. */
.choices {
  display: grid;
  gap: 0.5rem;
}
.choices-2 {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}
.choices-3 {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}
.choices-4 {
  grid-template-columns: repeat(4, minmax(0, 1fr));
}
@media (max-width: 1100px) {
  .choices-4 {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
.choice {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 0.125rem;
  padding: 0.625rem 0.75rem;
  text-align: left;
  border: 1px solid var(--border);
  border-radius: var(--radius-medium);
  cursor: pointer;
  transition: border-color 0.12s, background-color 0.12s;
}
.choice:hover {
  border-color: var(--color-border-strong);
}
.choice.disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.choice.on {
  border-color: var(--primary);
  background: color-mix(in srgb, var(--primary) 8%, transparent);
  box-shadow: inset 0 0 0 1px var(--primary);
}
.choice input {
  position: absolute;
  opacity: 0;
  pointer-events: none;
}
.choice:has(input:focus-visible) {
  outline: 2px solid var(--ring);
  outline-offset: 2px;
}
.choice-title {
  font-size: var(--text-7);
  font-weight: 600;
}
.choice.on .choice-title {
  color: var(--primary);
}
.choice-desc {
  font-size: var(--text-8);
  line-height: 1.4;
  color: var(--muted-foreground);
}

.savebar {
  position: sticky;
  bottom: 1rem;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.625rem 0.75rem 0.625rem 1.25rem;
  border: 1px solid var(--border);
  border-radius: var(--radius-large);
  background: var(--card);
  box-shadow: 0 8px 24px rgb(15 23 42 / 0.12);
  font-size: var(--text-7);
  font-weight: 500;
}
.savebar-enter-active,
.savebar-leave-active {
  transition: opacity 0.15s, transform 0.15s;
}
.savebar-enter-from,
.savebar-leave-to {
  opacity: 0;
  transform: translateY(0.5rem);
}

@media (max-width: 720px) {
  .srow {
    grid-template-columns: minmax(0, 1fr);
    padding: 1rem;
  }
  .section header {
    padding: 1rem;
  }
  .choices-2,
  .choices-3,
  .choices-4 {
    grid-template-columns: minmax(0, 1fr);
  }
}
@media (prefers-reduced-motion: reduce) {
  .choice,
  .savebar-enter-active,
  .savebar-leave-active {
    transition: none;
  }
}
</style>
