<script setup lang="ts">
// IVRNodeProperties — right-side panel that edits the config of the
// currently selected node. Faithful port of Whatomate's IVRNodeProperties
// adapted to Tring's stack (plain Tailwind, no shadcn-vue/i18n). Per-
// node-type config blocks cover all 8 IVR node types.
import { computed, ref, watch } from "vue";
import { Trash2, Volume2, Grid3X3, Hash, Globe, Users, ExternalLink, Clock, PhoneOff, Plus, X } from "lucide-vue-next";
import type { IVRNodeType, Flow } from "../../api/types";

interface VueFlowNode {
  id: string;
  type: IVRNodeType | string;
  data?: {
    label?: string;
    config?: Record<string, any>;
    isEntryNode?: boolean;
  };
  position?: { x: number; y: number };
}

const props = defineProps<{
  node: VueFlowNode;
  currentFlowId?: string;
  availableFlows?: Flow[];
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
        <label class="block">
          <span class="text-xs font-medium text-gray-700">Audio file (path/URL)</span>
          <input
            v-model="config.audio_file"
            type="text"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm font-mono"
            placeholder="/recordings/greeting.wav"
          />
        </label>
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
        <label class="block">
          <span class="text-xs font-medium text-gray-700">Audio file (path/URL)</span>
          <input
            v-model="config.audio_file"
            type="text"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm font-mono"
          />
        </label>
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
        <label class="block">
          <span class="text-xs font-medium text-gray-700">Audio file (path/URL)</span>
          <input
            v-model="config.audio_file"
            type="text"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm font-mono"
          />
        </label>
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
          <span class="text-xs font-medium text-gray-700">Agent phone number</span>
          <input
            v-model="config.agent_number"
            type="tel"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm font-mono"
            placeholder="+919876543210"
          />
          <span class="text-[11px] text-gray-400"
            >Rings this number via the trunk; bridges on answer.</span
          >
        </label>
        <label class="block">
          <span class="text-xs font-medium text-gray-700">Ring timeout (s)</span>
          <input
            v-model.number="config.ring_timeout"
            type="number"
            min="5"
            class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
            placeholder="30"
          />
        </label>
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
