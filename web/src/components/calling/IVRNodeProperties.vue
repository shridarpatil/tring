<script setup lang="ts">
// IVRNodeProperties — right-side panel that edits the config of the
// currently selected node. Faithful port of Whatomate's IVRNodeProperties
// adapted to Tring's stack (plain Tailwind, no shadcn-vue/i18n). Per-
// node-type config blocks cover all 8 IVR node types.
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

const colorFor: Record<string, string> = {
  greeting: "bg-green-600",
  menu: "bg-purple-600",
  gather: "bg-blue-600",
  http_callback: "bg-orange-600",
  transfer: "bg-amber-600",
  goto_flow: "bg-teal-600",
  timing: "bg-cyan-600",
  hangup: "bg-red-600",
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

// ─── Transfer target (team | specific agent | all) ──────────────────
// One select drives two mutually-exclusive config keys: config.team (a team
// name) or config.agent_sip (one agent). Empty = ring all available agents.
const transferTarget = computed<string>({
  get() {
    if (config.value.team) return `team:${config.value.team}`;
    if (config.value.agent_sip) return `agent:${config.value.agent_sip}`;
    return "";
  },
  set(v: string) {
    const next = { ...config.value };
    delete next.team;
    delete next.agent_sip;
    if (v.startsWith("team:")) next.team = v.slice(5);
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
  <div class="h-full flex flex-col">
    <!-- Header -->
    <div class="px-4 py-3 border-b border-gray-200 flex items-center gap-3">
      <div
        :class="[
          'h-8 w-8 rounded-md flex items-center justify-center text-white shrink-0',
          colorFor[node.type] || 'bg-gray-600',
        ]"
      >
        <component :is="iconFor[node.type] || PhoneOff" class="h-4 w-4" />
      </div>
      <div class="flex-1 min-w-0">
        <div class="text-[10px] uppercase tracking-wide text-gray-400 font-medium">
          {{ node.type.replace("_", " ") }}
        </div>
        <input
          v-model="label"
          class="w-full text-sm font-semibold bg-transparent border-0 p-0 focus:outline-none focus:ring-0"
          placeholder="Node label"
        />
      </div>
      <button
        class="h-7 w-7 rounded-md text-red-600 hover:bg-red-50 flex items-center justify-center"
        title="Delete node"
        @click="$emit('delete')"
      >
        <Trash2 class="h-4 w-4" />
      </button>
    </div>

    <div class="flex-1 overflow-y-auto px-4 py-4 space-y-4 text-sm">
      <!-- ── GREETING ──────────────────────────────────────────── -->
      <template v-if="node.type === 'greeting' || node.type === 'hangup'">
        <label class="block">
          <span class="text-xs font-medium text-gray-700">Greeting text</span>
          <textarea
            v-model="config.greeting_text"
            rows="3"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
            placeholder="Say this with TTS, or leave blank and use an audio file"
          />
        </label>
        <div class="block">
          <span class="text-xs font-medium text-gray-700">Audio file (from your sound library)</span>
          <AudioPicker v-model="config.audio_file" default-label="None (use text-to-speech above)" />
        </div>
        <label v-if="node.type === 'greeting'" class="flex items-center gap-2">
          <input v-model="config.interruptible" type="checkbox" class="rounded" />
          <span class="text-xs">Interruptible (caller can press a key to skip)</span>
        </label>
      </template>

      <!-- ── MENU ──────────────────────────────────────────────── -->
      <template v-if="node.type === 'menu'">
        <label class="block">
          <span class="text-xs font-medium text-gray-700">Prompt text</span>
          <textarea
            v-model="config.greeting_text"
            rows="2"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
            placeholder="Press 1 for sales, 2 for support…"
          />
        </label>
        <div class="block">
          <span class="text-xs font-medium text-gray-700">Audio file (from your sound library)</span>
          <AudioPicker v-model="config.audio_file" default-label="None (use prompt text above)" />
        </div>
        <div class="grid grid-cols-2 gap-3">
          <label class="block">
            <span class="text-xs font-medium text-gray-700">Timeout (s)</span>
            <input
              v-model.number="config.timeout_seconds"
              type="number"
              min="1"
              class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
            />
          </label>
          <label class="block">
            <span class="text-xs font-medium text-gray-700">Max retries</span>
            <input
              v-model.number="config.max_retries"
              type="number"
              min="0"
              class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
            />
          </label>
        </div>

        <div>
          <div class="text-xs font-medium text-gray-700 mb-2">Options</div>
          <div v-if="menuOptions.length === 0" class="text-xs text-gray-400 italic">
            No options yet — add a digit and label below.
          </div>
          <div
            v-for="[digit, opt] in menuOptions"
            :key="digit"
            class="flex items-center gap-2 mb-2"
          >
            <span
              class="h-7 w-7 rounded border border-gray-300 bg-gray-100 flex items-center justify-center font-mono font-bold text-sm shrink-0"
              >{{ digit }}</span
            >
            <input
              :value="opt.label"
              @input="
                (e) => {
                  const opts = { ...(config.options || {}) };
                  opts[digit] = { label: (e.target as HTMLInputElement).value };
                  config = { ...config, options: opts };
                }
              "
              class="flex-1 rounded-md border border-gray-300 px-2 py-1 text-sm"
              placeholder="Option label"
            />
            <button
              class="h-7 w-7 rounded text-red-600 hover:bg-red-50 flex items-center justify-center"
              @click="removeMenuOption(digit)"
            >
              <X class="h-3.5 w-3.5" />
            </button>
          </div>
          <div class="flex items-center gap-2 mt-2 pt-2 border-t border-gray-100">
            <input
              v-model="newDigit"
              maxlength="1"
              placeholder="0-9"
              class="h-7 w-12 rounded-md border border-gray-300 px-2 py-1 text-sm text-center font-mono"
            />
            <input
              v-model="newOptionLabel"
              placeholder="Label"
              class="flex-1 rounded-md border border-gray-300 px-2 py-1 text-sm"
            />
            <button
              class="h-7 w-7 rounded bg-gray-900 text-white hover:bg-gray-800 flex items-center justify-center"
              @click="addMenuOption"
            >
              <Plus class="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
      </template>

      <!-- ── GATHER ────────────────────────────────────────────── -->
      <template v-if="node.type === 'gather'">
        <label class="block">
          <span class="text-xs font-medium text-gray-700">Prompt text</span>
          <textarea
            v-model="config.greeting_text"
            rows="2"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
            placeholder="Please enter your account number followed by #"
          />
        </label>
        <div class="block">
          <span class="text-xs font-medium text-gray-700">Audio file (from your sound library)</span>
          <AudioPicker v-model="config.audio_file" default-label="None (use prompt text above)" />
        </div>
        <div class="grid grid-cols-2 gap-3">
          <label class="block">
            <span class="text-xs font-medium text-gray-700">Max digits</span>
            <input
              v-model.number="config.max_digits"
              type="number"
              min="1"
              max="50"
              class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
            />
          </label>
          <label class="block">
            <span class="text-xs font-medium text-gray-700">Terminator</span>
            <input
              v-model="config.terminator"
              maxlength="1"
              class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-center font-mono"
              placeholder="#"
            />
          </label>
          <label class="block">
            <span class="text-xs font-medium text-gray-700">Timeout (s)</span>
            <input
              v-model.number="config.timeout_seconds"
              type="number"
              min="1"
              class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
            />
          </label>
          <label class="block">
            <span class="text-xs font-medium text-gray-700">Max retries</span>
            <input
              v-model.number="config.max_retries"
              type="number"
              min="0"
              class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
            />
          </label>
        </div>
        <label class="block">
          <span class="text-xs font-medium text-gray-700">Store as (variable name)</span>
          <input
            v-model="config.store_as"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm font-mono"
            placeholder="account_number"
          />
        </label>
      </template>

      <!-- ── HTTP CALLBACK ─────────────────────────────────────── -->
      <template v-if="node.type === 'http_callback'">
        <div class="grid grid-cols-[80px_1fr] gap-2">
          <select
            v-model="config.method"
            class="rounded-md border border-gray-300 px-2 py-2 text-sm font-mono"
          >
            <option>GET</option>
            <option>POST</option>
            <option>PUT</option>
            <option>PATCH</option>
            <option>DELETE</option>
          </select>
          <input
            v-model="config.url"
            type="url"
            class="rounded-md border border-gray-300 px-3 py-2 text-sm font-mono"
            placeholder="https://your-crm/webhook"
          />
        </div>
        <label class="block">
          <span class="text-xs font-medium text-gray-700">Body template</span>
          <textarea
            v-model="config.body_template"
            rows="4"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-xs font-mono"
            placeholder='{"caller":"{{caller_id}}","digits":"{{account_number}}"}'
          />
        </label>
        <div>
          <div class="text-xs font-medium text-gray-700 mb-2">Headers</div>
          <div v-for="[k, v] in httpHeaders" :key="k" class="flex items-center gap-2 mb-2">
            <span class="font-mono text-xs flex-1 truncate">{{ k }}</span>
            <span class="font-mono text-xs flex-1 truncate text-gray-500">{{ v }}</span>
            <button
              class="h-6 w-6 rounded text-red-600 hover:bg-red-50 flex items-center justify-center"
              @click="removeHeader(k)"
            >
              <X class="h-3 w-3" />
            </button>
          </div>
          <div class="flex items-center gap-2 mt-2 pt-2 border-t border-gray-100">
            <input
              v-model="newHeaderKey"
              placeholder="Header"
              class="flex-1 rounded-md border border-gray-300 px-2 py-1 text-xs font-mono"
            />
            <input
              v-model="newHeaderVal"
              placeholder="Value"
              class="flex-1 rounded-md border border-gray-300 px-2 py-1 text-xs font-mono"
            />
            <button
              class="h-7 w-7 rounded bg-gray-900 text-white hover:bg-gray-800 flex items-center justify-center"
              @click="addHeader"
            >
              <Plus class="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
        <label class="block">
          <span class="text-xs font-medium text-gray-700">Timeout (s)</span>
          <input
            v-model.number="config.timeout_seconds"
            type="number"
            min="1"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
          />
        </label>
      </template>

      <!-- ── TRANSFER ──────────────────────────────────────────── -->
      <template v-if="node.type === 'transfer'">
        <label class="block">
          <span class="text-xs font-medium text-gray-700">Transfer to</span>
          <select
            v-model="transferTarget"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
          >
            <option value="">All available agents (ring everyone)</option>
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

        <!-- Queue explainer + Priority checkbox — only meaningful for a team. -->
        <template v-if="transferTarget.startsWith('team:')">
          <div class="rounded-md bg-blue-50 border border-blue-100 p-3 text-[11px] text-blue-800 leading-relaxed">
            <span class="font-medium">If everyone on the team is busy,</span> callers hear hold
            music and wait in line, then connect to the next agent who frees up. The team's
            strategy, wait time and hold music are set on the <strong>Teams</strong> page.
          </div>

          <label class="flex items-start gap-2">
            <input type="checkbox" v-model="config.priority" class="mt-0.5" />
            <span class="text-xs text-gray-700">
              <span class="font-medium">Priority caller — let them skip the line</span>
              <span class="block text-[11px] text-gray-400 mt-0.5">
                Callers reaching this step wait in a separate front-of-line queue. To route by
                who's calling, put an <strong>HTTP callback</strong> step before this and wire its
                <strong>2xx</strong> branch here (priority on) and its <strong>error</strong>
                branch to a normal Transfer.
              </span>
            </span>
          </label>
        </template>

        <p v-if="!availableTeams || !availableTeams.length" class="text-[11px] text-amber-600">
          No teams yet — create one under <strong>Teams</strong> to ring a group with a strategy.
        </p>
      </template>

      <!-- ── GOTO FLOW ─────────────────────────────────────────── -->
      <template v-if="node.type === 'goto_flow'">
        <label class="block">
          <span class="text-xs font-medium text-gray-700">Target flow</span>
          <select
            v-model="config.flow_id"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
          >
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
        <div class="text-xs font-medium text-gray-700 mb-1">Business hours</div>
        <div
          v-for="(s, idx) in (config.schedule || []) as Array<{day:string,enabled:boolean,start_time:string,end_time:string}>"
          :key="s.day"
          class="grid grid-cols-[80px_1fr_1fr_auto] gap-2 items-center"
        >
          <span class="text-xs font-medium capitalize">{{ s.day }}</span>
          <input
            v-model="s.start_time"
            type="time"
            :disabled="!s.enabled"
            class="rounded-md border border-gray-300 px-2 py-1 text-xs"
          />
          <input
            v-model="s.end_time"
            type="time"
            :disabled="!s.enabled"
            class="rounded-md border border-gray-300 px-2 py-1 text-xs"
          />
          <input
            v-model="s.enabled"
            type="checkbox"
            class="rounded"
            @change="config = { ...config, schedule: [...(config.schedule || [])] }"
            :data-idx="idx"
          />
        </div>
      </template>
    </div>
  </div>
</template>
