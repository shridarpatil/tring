<script setup lang="ts">
// IVRNodeProperties — right-side panel that edits the config of the
// currently selected node. Per-node-type config blocks cover all 8 IVR
// node types. Bare inputs are styled by Oat; only structural layout is scoped.
import { computed, ref, watch } from "vue";
import { Trash2, Volume2, Grid3X3, Hash, Globe, Users, ExternalLink, Clock, PhoneOff, Plus, X } from "lucide-vue-next";
import type { IVRNodeType, Flow, Agent, Team } from "../../api/types";
import AudioPicker from "../AudioPicker.vue";

interface VueFlowNode {
  id: string;
  type: IVRNodeType | string;
  data?: {
    label?: string;
    config?: Record<string, any>;
  };
  position?: { x: number; y: number };
}

const props = defineProps<{
  node: VueFlowNode;
  currentFlowId?: string;
  availableFlows?: Flow[];
  availableAgents?: Agent[];
  availableTeams?: Team[];
}>();

const emit = defineEmits<{
  (e: "update:node", node: VueFlowNode): void;
  (e: "delete"): void;
}>();

const iconFor: Record<string, any> = {
  greeting: Volume2,
  menu: Grid3X3,
  gather: Hash,
  http_callback: Globe,
  transfer: Users,
  goto_flow: ExternalLink,
  timing: Clock,
  hangup: PhoneOff,
};

// Node-type identity colours (concrete hex — not theme tokens).
const colorFor: Record<string, string> = {
  greeting: "#16a34a",
  menu: "#9333ea",
  gather: "#2563eb",
  http_callback: "#ea580c",
  transfer: "#d97706",
  goto_flow: "#0d9488",
  timing: "#0891b2",
  hangup: "#dc2626",
};

const label = ref(props.node.data?.label || "");
const config = ref<Record<string, any>>({ ...(props.node.data?.config || {}) });

watch(
  () => props.node.id,
  () => {
    label.value = props.node.data?.label || "";
    config.value = { ...(props.node.data?.config || {}) };
  },
);

function commit() {
  emit("update:node", {
    ...props.node,
    data: {
      ...(props.node.data || {}),
      label: label.value,
      config: { ...config.value },
    },
  });
}

watch(label, commit);
watch(config, commit, { deep: true });

// ─── Transfer target (team | specific agent | phone number | all) ───
// One select drives mutually-exclusive config keys: config.team (a team name),
// config.agent_sip (one agent) or config.number (dial a phone number, may hold
// {{vars}}). Empty = ring all available agents.
const transferTarget = computed<string>({
  get() {
    if (config.value.number !== undefined) return "number";
    if (config.value.team) return `team:${config.value.team}`;
    if (config.value.agent_sip) return `agent:${config.value.agent_sip}`;
    return "";
  },
  set(v: string) {
    const next = { ...config.value };
    delete next.team;
    delete next.agent_sip;
    delete next.number;
    delete next.ring_seconds;
    if (v === "number") next.number = "";
    else if (v.startsWith("team:")) next.team = v.slice(5);
    else if (v.startsWith("agent:")) next.agent_sip = v.slice(6);
    config.value = next;
  },
});

// ─── Menu options editor ────────────────────────────────────────────
const menuOptions = computed<[string, { label: string }][]>(() => {
  const opts = (config.value.options || {}) as Record<string, { label: string }>;
  return Object.entries(opts).sort(([a], [b]) => a.localeCompare(b));
});
const newDigit = ref("");
const newOptionLabel = ref("");
function addMenuOption() {
  if (!newDigit.value || !/^[0-9*#]$/.test(newDigit.value)) return;
  const opts = (config.value.options || {}) as Record<string, { label: string }>;
  opts[newDigit.value] = { label: newOptionLabel.value || `Option ${newDigit.value}` };
  config.value = { ...config.value, options: { ...opts } };
  newDigit.value = "";
  newOptionLabel.value = "";
}
function removeMenuOption(d: string) {
  const opts = { ...((config.value.options || {}) as Record<string, { label: string }>) };
  delete opts[d];
  config.value = { ...config.value, options: opts };
}

// ─── HTTP headers editor ────────────────────────────────────────────
const httpHeaders = computed<[string, string][]>(() => {
  const h = (config.value.headers || {}) as Record<string, string>;
  return Object.entries(h);
});
const newHeaderKey = ref("");
const newHeaderVal = ref("");
function addHeader() {
  if (!newHeaderKey.value) return;
  const h = { ...((config.value.headers || {}) as Record<string, string>) };
  h[newHeaderKey.value] = newHeaderVal.value;
  config.value = { ...config.value, headers: h };
  newHeaderKey.value = "";
  newHeaderVal.value = "";
}
function removeHeader(k: string) {
  const h = { ...((config.value.headers || {}) as Record<string, string>) };
  delete h[k];
  config.value = { ...config.value, headers: h };
}

// ─── Timing schedule defaults ──────────────────────────────────────
const days = ["monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"] as const;
if (props.node.type === "timing" && !Array.isArray(config.value.schedule)) {
  config.value.schedule = days.map((day) => ({
    day,
    enabled: day !== "saturday" && day !== "sunday",
    start_time: "09:00",
    end_time: "17:00",
  }));
}
</script>

<template>
  <div class="props">
    <!-- Header -->
    <div class="props-head">
      <div class="type-badge" :style="{ background: colorFor[node.type] || 'var(--secondary)' }">
        <component :is="iconFor[node.type] || PhoneOff" :size="16" />
      </div>
      <div class="head-mid">
        <div class="type-kicker">{{ node.type.replace("_", " ") }}</div>
        <input v-model="label" class="label-input" placeholder="Node label" />
      </div>
      <button type="button" class="mini-del" title="Delete node" @click="$emit('delete')">
        <Trash2 :size="16" />
      </button>
    </div>

    <div class="props-body">
      <!-- ── GREETING ──────────────────────────────────────────── -->
      <template v-if="node.type === 'greeting' || node.type === 'hangup'">
        <label class="fld">
          <span class="fl">Greeting text</span>
          <textarea
            v-model="config.greeting_text"
            rows="3"
            placeholder="Say this with TTS, or leave blank and use an audio file"
          />
        </label>
        <div>
          <span class="fl">Audio file (from your sound library)</span>
          <AudioPicker v-model="config.audio_file" default-label="None (use text-to-speech above)" />
        </div>
        <label v-if="node.type === 'greeting'" class="chk">
          <input v-model="config.interruptible" type="checkbox" />
          <span>Interruptible (caller can press a key to skip)</span>
        </label>
      </template>

      <!-- ── MENU ──────────────────────────────────────────────── -->
      <template v-if="node.type === 'menu'">
        <label class="fld">
          <span class="fl">Prompt text</span>
          <textarea v-model="config.greeting_text" rows="2" placeholder="Press 1 for sales, 2 for support…" />
        </label>
        <div>
          <span class="fl">Audio file (from your sound library)</span>
          <AudioPicker v-model="config.audio_file" default-label="None (use prompt text above)" />
        </div>
        <div class="grid grid-2">
          <label class="fld">
            <span class="fl">Timeout (s)</span>
            <input v-model.number="config.timeout_seconds" type="number" min="1" />
          </label>
          <label class="fld">
            <span class="fl">Max retries</span>
            <input v-model.number="config.max_retries" type="number" min="0" />
          </label>
        </div>

        <div>
          <div class="fl" style="margin-bottom: 0.5rem">Options</div>
          <div v-if="menuOptions.length === 0" class="empty-note">
            No options yet — add a digit and label below.
          </div>
          <div v-for="[digit, opt] in menuOptions" :key="digit" class="edit-row">
            <span class="digit-chip">{{ digit }}</span>
            <input
              :value="opt.label"
              placeholder="Option label"
              @input="
                (e) => {
                  const opts = { ...(config.options || {}) };
                  opts[digit] = { label: (e.target as HTMLInputElement).value };
                  config = { ...config, options: opts };
                }
              "
            />
            <button type="button" class="mini-del" @click="removeMenuOption(digit)">
              <X :size="14" />
            </button>
          </div>
          <div class="edit-row add-row">
            <input v-model="newDigit" maxlength="1" placeholder="0-9" class="mono digit-in" />
            <input v-model="newOptionLabel" placeholder="Label" />
            <button type="button" class="mini-add" @click="addMenuOption"><Plus :size="14" /></button>
          </div>
        </div>
      </template>

      <!-- ── GATHER ────────────────────────────────────────────── -->
      <template v-if="node.type === 'gather'">
        <label class="fld">
          <span class="fl">Prompt text</span>
          <textarea v-model="config.greeting_text" rows="2" placeholder="Please enter your account number followed by #" />
        </label>
        <div>
          <span class="fl">Audio file (from your sound library)</span>
          <AudioPicker v-model="config.audio_file" default-label="None (use prompt text above)" />
        </div>
        <div class="grid grid-2">
          <label class="fld">
            <span class="fl">Max digits</span>
            <input v-model.number="config.max_digits" type="number" min="1" max="50" />
          </label>
          <label class="fld">
            <span class="fl">Terminator</span>
            <input v-model="config.terminator" maxlength="1" class="mono" style="text-align: center" placeholder="#" />
          </label>
          <label class="fld">
            <span class="fl">Timeout (s)</span>
            <input v-model.number="config.timeout_seconds" type="number" min="1" />
          </label>
          <label class="fld">
            <span class="fl">Max retries</span>
            <input v-model.number="config.max_retries" type="number" min="0" />
          </label>
        </div>
        <label class="fld">
          <span class="fl">Store as (variable name)</span>
          <input v-model="config.store_as" class="mono" placeholder="account_number" />
        </label>
      </template>

      <!-- ── HTTP CALLBACK ─────────────────────────────────────── -->
      <template v-if="node.type === 'http_callback'">
        <div class="g-http">
          <select v-model="config.method" class="mono">
            <option>GET</option>
            <option>POST</option>
            <option>PUT</option>
            <option>PATCH</option>
            <option>DELETE</option>
          </select>
          <input v-model="config.url" type="url" class="mono" placeholder="https://your-crm/webhook" />
        </div>
        <label class="fld">
          <span class="fl">Body template</span>
          <textarea
            v-model="config.body_template"
            rows="4"
            class="mono"
            placeholder='{"caller":"{{caller_id}}","digits":"{{account_number}}"}'
          />
        </label>
        <div>
          <div class="fl" style="margin-bottom: 0.5rem">Headers</div>
          <div v-for="[k, v] in httpHeaders" :key="k" class="edit-row">
            <span class="mono truncate hdr-cell">{{ k }}</span>
            <span class="mono truncate hdr-cell text-light">{{ v }}</span>
            <button type="button" class="mini-del" @click="removeHeader(k)"><X :size="12" /></button>
          </div>
          <div class="edit-row add-row">
            <input v-model="newHeaderKey" placeholder="Header" class="mono" />
            <input v-model="newHeaderVal" placeholder="Value" class="mono" />
            <button type="button" class="mini-add" @click="addHeader"><Plus :size="14" /></button>
          </div>
        </div>
        <label class="fld">
          <span class="fl">Timeout (s)</span>
          <input v-model.number="config.timeout_seconds" type="number" min="1" />
        </label>
      </template>

      <!-- ── TRANSFER ──────────────────────────────────────────── -->
      <template v-if="node.type === 'transfer'">
        <label class="fld">
          <span class="fl">Transfer to</span>
          <select v-model="transferTarget">
            <option value="">All available agents (ring everyone)</option>
            <option value="number">Phone number…</option>
            <optgroup v-if="availableTeams && availableTeams.length" label="Teams">
              <option v-for="t in availableTeams" :key="t.name" :value="`team:${t.name}`">
                {{ t.name }} ({{ t.strategy }})
              </option>
            </optgroup>
            <optgroup v-if="availableAgents && availableAgents.length" label="Specific agent">
              <option v-for="a in availableAgents" :key="a.id" :value="`agent:${a.sip_username}`">
                {{ a.display_name || a.sip_username }}
              </option>
            </optgroup>
          </select>
        </label>

        <!-- Phone number: dialed over the outbound trunk; answer → completed. -->
        <template v-if="transferTarget === 'number'">
          <div class="grid grid-2">
            <label class="fld">
              <span class="fl">Number to dial</span>
              <input v-model="config.number" class="mono" placeholder="+919812345678 or {{dest}}" />
            </label>
            <label class="fld">
              <span class="fl">Ring for (s)</span>
              <input v-model.number="config.ring_seconds" type="number" min="5" max="120" placeholder="30" />
            </label>
          </div>
          <p class="sub-note">
            Use a variable such as <code v-pre>{{dest}}</code> to dial a number returned by an earlier
            <strong>HTTP callback</strong>. The caller sees one of your outbound numbers and hears your hold music while it rings.
            Busy, no answer or a bad number takes the <strong>no answer</strong> branch.
          </p>
          <p v-if="!String(config.number || '').trim()" class="warn-note">Enter a number or variable to dial.</p>
        </template>

        <!-- Queue explainer + Priority checkbox — only meaningful for a team. -->
        <template v-if="transferTarget.startsWith('team:')">
          <div class="info-box">
            <span style="font-weight: 500">If everyone on the team is busy,</span> callers hear hold
            music and wait in line, then connect to the next agent who frees up. The team's
            strategy, wait time and hold music are set on the <strong>Teams</strong> page.
          </div>

          <label class="chk chk-top">
            <input type="checkbox" v-model="config.priority" />
            <span class="text-light">
              <span style="font-weight: 500; color: var(--foreground)">Priority caller — let them skip the line</span>
              <span class="sub-note">
                Callers reaching this step wait in a separate front-of-line queue. To route by
                who's calling, put an <strong>HTTP callback</strong> step before this and wire its
                <strong>2xx</strong> branch here (priority on) and its <strong>error</strong>
                branch to a normal Transfer.
              </span>
            </span>
          </label>
        </template>

        <p v-if="transferTarget !== 'number' && (!availableTeams || !availableTeams.length)" class="warn-note">
          No teams yet — create one under <strong>Teams</strong> to ring a group with a strategy.
        </p>
      </template>

      <!-- ── GOTO FLOW ─────────────────────────────────────────── -->
      <template v-if="node.type === 'goto_flow'">
        <label class="fld">
          <span class="fl">Target flow</span>
          <select v-model="config.flow_id">
            <option value="">— Select a flow —</option>
            <option
              v-for="f in (availableFlows || []).filter((f) => f.id !== currentFlowId)"
              :key="f.id"
              :value="f.id"
            >
              {{ f.name }}
            </option>
          </select>
        </label>
      </template>

      <!-- ── TIMING ────────────────────────────────────────────── -->
      <template v-if="node.type === 'timing'">
        <div class="fl" style="margin-bottom: 0.25rem">Business hours</div>
        <div
          v-for="(s, idx) in (config.schedule || []) as Array<{day:string,enabled:boolean,start_time:string,end_time:string}>"
          :key="s.day"
          class="g-timing"
        >
          <span class="day-name">{{ s.day }}</span>
          <input v-model="s.start_time" type="time" :disabled="!s.enabled" />
          <input v-model="s.end_time" type="time" :disabled="!s.enabled" />
          <input
            v-model="s.enabled"
            type="checkbox"
            :data-idx="idx"
            @change="config = { ...config, schedule: [...(config.schedule || [])] }"
          />
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.props { height: 100%; display: flex; flex-direction: column; }
.props-head {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.75rem 1rem;
  border-bottom: 1px solid var(--border);
}
.type-badge {
  height: 2rem;
  width: 2rem;
  border-radius: var(--radius-medium);
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  flex-shrink: 0;
}
.head-mid { flex: 1; min-width: 0; }
.type-kicker {
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--faint-foreground);
  font-weight: 500;
}
.label-input {
  width: 100%;
  font-size: var(--text-7);
  font-weight: 600;
  background: transparent;
  border: none;
  padding: 0;
  margin: 0;
  color: var(--foreground);
}
.label-input:focus { outline: none; box-shadow: none; }
.props-body {
  flex: 1;
  overflow-y: auto;
  padding: 1rem;
  display: flex;
  flex-direction: column;
  gap: 1rem;
  font-size: var(--text-7);
}
.fld { display: block; }
.fld > input,
.fld > textarea,
.fld > select { margin-top: 0.25rem; }
.fl { display: block; font-size: var(--text-8); font-weight: 500; color: var(--muted-foreground); }
.chk { display: flex; align-items: center; gap: 0.5rem; font-size: var(--text-8); }
.chk-top { align-items: flex-start; }
.chk-top input { margin-top: 0.15rem; }
.sub-note { display: block; font-size: 11px; color: var(--faint-foreground); margin-top: 0.15rem; }
.empty-note { font-size: var(--text-8); color: var(--faint-foreground); font-style: italic; }
.edit-row { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; }
.edit-row > input { margin-top: 0; }
.add-row { margin-top: 0.5rem; padding-top: 0.5rem; border-top: 1px solid var(--border); }
.digit-in { width: 3rem; text-align: center; flex: none; }
.digit-chip {
  height: 1.75rem;
  width: 1.75rem;
  border-radius: var(--radius-medium);
  border: 1px solid var(--border);
  background: var(--secondary);
  display: flex;
  align-items: center;
  justify-content: center;
  font-family: var(--font-mono);
  font-weight: 700;
  flex-shrink: 0;
}
.hdr-cell { flex: 1; font-size: var(--text-8); }
.mini-del {
  height: 1.75rem;
  width: 1.75rem;
  padding: 0;
  background: transparent;
  border: none;
  border-radius: var(--radius-medium);
  color: var(--danger);
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  flex-shrink: 0;
}
.mini-del:hover { background: var(--color-danger-soft); }
.mini-add {
  height: 1.75rem;
  width: 1.75rem;
  padding: 0;
  border: none;
  border-radius: var(--radius-medium);
  background: var(--primary);
  color: var(--primary-foreground);
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  flex-shrink: 0;
}
.info-box {
  border-radius: var(--radius-medium);
  padding: 0.75rem;
  background: color-mix(in srgb, var(--primary) 10%, transparent);
  border: 1px solid color-mix(in srgb, var(--primary) 30%, transparent);
  font-size: 11px;
  color: var(--muted-foreground);
  line-height: 1.5;
}
.warn-note { font-size: 11px; color: var(--warning); }
.g-http { display: grid; grid-template-columns: 80px 1fr; gap: 0.5rem; }
.g-http > select, .g-http > input { margin-top: 0; }
.g-timing { display: grid; grid-template-columns: 80px 1fr 1fr auto; gap: 0.5rem; align-items: center; }
.g-timing > input { margin-top: 0; }
.day-name { font-size: var(--text-8); font-weight: 500; text-transform: capitalize; }
</style>
