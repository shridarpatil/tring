<script setup lang="ts">
// FlowEditorView — the IVR flow editor. Combines what Whatomate splits
// across FlowBuilder + IVRFlowEditorView into one Tring view:
//   • Top toolbar: name input, active/call-start toggles, save button
//   • Palette: 8 node types (click to add at viewport center)
//   • Main: vue-flow canvas (drag/connect) + properties panel on the right
// Uses localStorage as the persistence stub until the backend lands.
import { ref, computed, onMounted, markRaw, watch } from "vue";
import { errMessage } from "../../lib/errors";
import { useRoute, useRouter } from "vue-router";
import {
  VueFlow,
  useVueFlow,
  MarkerType,
  type NodeMouseEvent,
  type Edge,
} from "@vue-flow/core";
import { Background } from "@vue-flow/background";
import { Controls } from "@vue-flow/controls";
import { MiniMap } from "@vue-flow/minimap";
import {
  ArrowLeft,
  Save,
  Volume2,
  Grid3X3,
  Hash,
  Globe,
  Users,
  ExternalLink,
  Clock,
  PhoneOff,
  PlayCircle,
} from "lucide-vue-next";
import { Button, IconButton, Toggle } from "../../components/ui";
import { theme } from "../../lib/theme";

// Canvas dot-grid tracks the theme (a dark hex would be harsh on a light pane).
const bgPattern = computed(() => (theme.state.theme === "dark" ? "#2a323d" : "#d8dce1"));

import "@vue-flow/core/dist/style.css";
import "@vue-flow/core/dist/theme-default.css";
import "@vue-flow/controls/dist/style.css";
import "@vue-flow/minimap/dist/style.css";

import { api } from "../../api/client";
import GreetingNode from "../../components/calling/nodes/GreetingNode.vue";
import StartNode from "../../components/calling/nodes/StartNode.vue";
import MenuNode from "../../components/calling/nodes/MenuNode.vue";
import GatherNode from "../../components/calling/nodes/GatherNode.vue";
import HTTPCallbackNode from "../../components/calling/nodes/HTTPCallbackNode.vue";
import TransferNode from "../../components/calling/nodes/TransferNode.vue";
import GotoFlowNode from "../../components/calling/nodes/GotoFlowNode.vue";
import TimingNode from "../../components/calling/nodes/TimingNode.vue";
import HangupNode from "../../components/calling/nodes/HangupNode.vue";
import IVRNodeProperties from "../../components/calling/IVRNodeProperties.vue";

import type { Flow, IVRFlowData, IVRNodeType, Agent, Team, Paginated } from "../../api/types";

const route = useRoute();
const router = useRouter();
const flowId = computed(() => route.params.id as string);

const flowName = ref("");
const isActive = ref(true);
const isOutgoingEnd = ref(false);
const saving = ref(false);

// ─── Simulator panel ───────────────────────────────────────────────
// Click "Test" → showSimPanel toggles a right-side drawer that drives
// the flow interactively. Walker is server-side and pauses on every
// Menu / Gather node, surfacing an awaiting_node with a prompt. The
// UI then asks the operator for a digit and POSTs it back.
const showSimPanel = ref(false);
const simBusy = ref(false);
const simCaller = ref("+10000000000");
const simInput = ref("");
const simError = ref("");
const simSession = ref<SimSnapshot | null>(null);

interface SimStep {
  node_id: string;
  node_type: string;
  action: string;
  detail?: string;
  dtmf_input?: string;
  outcome: string;
}
interface AwaitingNode {
  node_id: string;
  node_type: string;
  prompt?: string;
}
interface SimSnapshot {
  session_id: string;
  status: "awaiting_dtmf" | "ended" | "error";
  steps: SimStep[];
  vars: Record<string, string>;
  awaiting_node?: AwaitingNode;
  ended_at?: string;
  hangup_cause?: string;
  error?: string;
}

async function startSimulation() {
  simBusy.value = true;
  simError.value = "";
  simSession.value = null;
  simInput.value = "";
  // Persist the on-canvas edits first so the simulator runs the version
  // the operator can see. Failures during save don't block the run.
  try {
    await saveFlow();
  } catch {
    /* fall through */
  }
  try {
    simSession.value = await api.post<SimSnapshot>(
      `/v1/flows/${flowId.value}/simulate/start`,
      { caller: simCaller.value || undefined },
    );
  } catch (e) {
    simError.value = errMessage(e, "start failed");
  } finally {
    simBusy.value = false;
  }
}

async function sendSimInput() {
  if (!simSession.value || simSession.value.status !== "awaiting_dtmf") return;
  if (!simInput.value) return;
  simBusy.value = true;
  simError.value = "";
  const sid = simSession.value.session_id;
  const dtmf = simInput.value;
  simInput.value = "";
  try {
    simSession.value = await api.post<SimSnapshot>(
      `/v1/flows/simulate/sessions/${sid}/input`,
      { dtmf },
    );
  } catch (e) {
    simError.value = errMessage(e, "send failed");
  } finally {
    simBusy.value = false;
  }
}
const savedMsg = ref("");

// Vue Flow custom node types
const nodeTypes: Record<string, any> = {
  start: markRaw(StartNode),
  greeting: markRaw(GreetingNode),
  menu: markRaw(MenuNode),
  gather: markRaw(GatherNode),
  http_callback: markRaw(HTTPCallbackNode),
  transfer: markRaw(TransferNode),
  goto_flow: markRaw(GotoFlowNode),
  timing: markRaw(TimingNode),
  hangup: markRaw(HangupNode),
};

// Palette (left-to-right order matches Whatomate)
const palette: { type: IVRNodeType; label: string; icon: any; color: string }[] = [
  { type: "greeting", label: "Greeting", icon: Volume2, color: "bg-green-600" },
  { type: "menu", label: "Menu", icon: Grid3X3, color: "bg-purple-600" },
  { type: "gather", label: "Gather", icon: Hash, color: "bg-blue-600" },
  { type: "http_callback", label: "HTTP", icon: Globe, color: "bg-orange-600" },
  { type: "transfer", label: "Transfer", icon: Users, color: "bg-amber-600" },
  { type: "goto_flow", label: "Goto Flow", icon: ExternalLink, color: "bg-teal-600" },
  { type: "timing", label: "Timing", icon: Clock, color: "bg-cyan-600" },
  { type: "hangup", label: "Hangup", icon: PhoneOff, color: "bg-red-600" },
];

const {
  nodes,
  edges,
  addNodes,
  addEdges,
  removeNodes,
  removeEdges,
  onConnect,
  project,
} = useVueFlow({
  defaultEdgeOptions: {
    type: "default",
    animated: true,
    markerEnd: MarkerType.ArrowClosed,
  },
});

const selectedNodeId = ref<string | null>(null);
const selectedNode = computed(() => {
  if (!selectedNodeId.value) return null;
  return nodes.value.find((n) => n.id === selectedNodeId.value) || null;
});

function onNodeClick(event: NodeMouseEvent) {
  selectedNodeId.value = event.node.id;
}
function onPaneClick() {
  selectedNodeId.value = null;
}

// Every flow has exactly one Start node with this fixed ID. Pinning
// the ID means we don't need to track "which node is the entry" in
// reactive state — `entry_node` in the persisted flow always equals
// START_NODE_ID, and the Start node's single "default" outgoing edge
// is what the executor walks first.
const START_NODE_ID = "node_start";
let nodeCounter = 0;

function ensureStartNode() {
  if (nodes.value.some((n) => n.id === START_NODE_ID)) return;
  addNodes([
    {
      id: START_NODE_ID,
      type: "start",
      position: { x: 80, y: 80 },
      data: { label: "Start", config: {} },
      deletable: false,
    },
  ]);
}

const defaultConfigs: Record<IVRNodeType, Record<string, any>> = {
  // Start has no configurable settings — it's a pure passthrough.
  start: {},
  greeting: { audio_file: "", greeting_text: "", interruptible: false },
  menu: { audio_file: "", greeting_text: "", timeout_seconds: 10, max_retries: 3, options: {} },
  gather: {
    audio_file: "",
    greeting_text: "",
    min_digits: 1,
    max_digits: 10,
    terminator: "#",
    timeout_seconds: 10,
    max_retries: 3,
    store_as: "",
  },
  http_callback: { url: "", method: "GET", headers: {}, body_template: "", timeout_seconds: 10 },
  transfer: { team: "", agent_sip: "", priority: false },
  goto_flow: { flow_id: "" },
  timing: {
    schedule: [
      { day: "monday", enabled: true, start_time: "09:00", end_time: "17:00" },
      { day: "tuesday", enabled: true, start_time: "09:00", end_time: "17:00" },
      { day: "wednesday", enabled: true, start_time: "09:00", end_time: "17:00" },
      { day: "thursday", enabled: true, start_time: "09:00", end_time: "17:00" },
      { day: "friday", enabled: true, start_time: "09:00", end_time: "17:00" },
      { day: "saturday", enabled: false, start_time: "09:00", end_time: "17:00" },
      { day: "sunday", enabled: false, start_time: "09:00", end_time: "17:00" },
    ],
  },
  hangup: { audio_file: "", greeting_text: "" },
};

function addNodeFromPalette(type: IVRNodeType) {
  const pos = project({
    x: window.innerWidth / 2 - 280,
    y: window.innerHeight / 2 - 100,
  });
  const id = `node_${Date.now()}_${nodeCounter++}`;
  addNodes([
    {
      id,
      type,
      position: { x: pos.x, y: pos.y },
      data: {
        label: type.replace("_", " ").replace(/\b\w/g, (c: string) => c.toUpperCase()),
        config: { ...(defaultConfigs[type] || {}) },
      },
    },
  ]);
}

// Connection-validity rule, fed to <VueFlow :is-valid-connection="...">.
// Vue Flow's default *should* already reject source→source, but be
// explicit so this can't silently regress on a version bump:
//
//   - targetHandle must be the input handle ("input"), never an
//     output handle id like "default" / "digit:1" / "timeout"
//   - source and target nodes must differ (no self-loops)
//
// Returning false aborts the drop and the wire snaps back.
function isValidConnection(c: { source: string; target: string; targetHandle?: string | null }) {
  if (c.source === c.target) return false;
  return c.targetHandle === "input";
}

// Connections: enforce single edge per source handle. The branch
// identity is already visible on the node (handle label under each
// output dot), so the connector itself stays unlabelled.
onConnect((params) => {
  const existing = edges.value.filter(
    (e) => e.source === params.source && e.sourceHandle === params.sourceHandle,
  );
  if (existing.length > 0) removeEdges(existing);
  addEdges([
    {
      ...params,
      type: "default",
      animated: true,
      markerEnd: MarkerType.ArrowClosed,
    },
  ]);
});

function onUpdateNode(updated: any) {
  const idx = nodes.value.findIndex((n) => n.id === updated.id);
  if (idx >= 0) {
    nodes.value[idx] = {
      ...nodes.value[idx],
      data: { ...nodes.value[idx].data, ...updated.data },
    };
  }
}

function requestDeleteSelectedNode() {
  if (!selectedNode.value) return;
  // Start is the implicit entry point — every flow has exactly one.
  // Hard-block its deletion rather than silently breaking the flow.
  if (selectedNode.value.id === START_NODE_ID) {
    alert("The Start node is the flow's entry point and can't be removed.");
    return;
  }
  if (!confirm(`Delete node "${selectedNode.value.data?.label || selectedNode.value.type}"?`))
    return;
  const id = selectedNode.value.id;
  removeNodes([selectedNode.value]);
  removeEdges(edges.value.filter((e) => e.source === id || e.target === id));
  selectedNodeId.value = null;
}

// ─── Persistence (Tring API) ────────────────────────────────────────
const availableFlows = ref<Flow[]>([]);
// Agents are offered as Transfer-node targets ("ring a specific agent").
const availableAgents = ref<Agent[]>([]);
// Teams are the primary Transfer-node target ("ring this team by its strategy").
const availableTeams = ref<Team[]>([]);

function buildIVRFlowData(): IVRFlowData {
  return {
    version: 2,
    // Start is always the entry; the executor walks its single
    // outgoing edge to find the real first step.
    entry_node: START_NODE_ID,
    nodes: nodes.value.map((n) => ({
      id: n.id,
      type: (n.type || "greeting") as IVRNodeType,
      label: n.data?.label || "",
      position: { x: n.position.x, y: n.position.y },
      config: (n.data?.config || {}) as Record<string, unknown>,
    })),
    edges: edges.value.map((e) => ({
      from: e.source,
      to: e.target,
      condition: (e.sourceHandle as string) || "default",
    })),
  };
}

function loadFromIVRFlowData(d: IVRFlowData) {
  removeNodes(nodes.value);
  removeEdges(edges.value);
  for (const n of d.nodes) {
    addNodes([
      {
        id: n.id,
        type: n.type,
        position: n.position,
        data: { label: n.label, config: n.config },
        deletable: n.type !== "start",
      },
    ]);
  }
  for (const e of d.edges) {
    addEdges([
      {
        id: `e_${e.from}_${e.condition}_${e.to}`,
        source: e.from,
        target: e.to,
        sourceHandle: e.condition,
        // Every node's input handle is hard-coded to id "input" in
        // BaseNode.vue. Without targetHandle set, Vue Flow can't bind
        // the edge end to the target node's input pin, so the edge
        // renders as floating / hidden — which looked like "saves
        // weren't persisting" on reload.
        targetHandle: "input",
        type: "default",
        animated: true,
        markerEnd: MarkerType.ArrowClosed,
      },
    ]);
  }
  // Migration for flows saved before Start nodes existed: ensure one
  // is present, and if the persisted entry_node points at a real node,
  // wire Start → that node so the executor reaches it.
  ensureStartNode();
  if (d.entry_node && d.entry_node !== START_NODE_ID && nodes.value.find((n) => n.id === d.entry_node)) {
    const alreadyWired = edges.value.some(
      (e) => e.source === START_NODE_ID && e.target === d.entry_node,
    );
    if (!alreadyWired) {
      addEdges([
        {
          id: `e_${START_NODE_ID}_default_${d.entry_node}`,
          source: START_NODE_ID,
          target: d.entry_node,
          sourceHandle: "default",
          type: "default",
          animated: true,
          markerEnd: MarkerType.ArrowClosed,
        },
      ]);
    }
  }
}

// syncRenderedAudio pulls the server's freshly-rendered prompt audio back into
// the canvas. On save the API synthesizes node text (greeting_text) to an audio
// file and stamps each node's audio_file; without copying that back, the play
// button keeps the stale/empty path until a page refresh.
function syncRenderedAudio(def?: IVRFlowData) {
  if (!def?.nodes) return;
  for (const sn of def.nodes) {
    const idx = nodes.value.findIndex((n) => n.id === sn.id);
    if (idx < 0) continue;
    const serverAF = ((sn.config as Record<string, unknown>)?.audio_file as string) || "";
    const cfg = (nodes.value[idx].data?.config || {}) as Record<string, unknown>;
    if (((cfg.audio_file as string) || "") === serverAF) continue;
    nodes.value[idx] = {
      ...nodes.value[idx],
      data: { ...nodes.value[idx].data, config: { ...cfg, audio_file: serverAF } },
    };
  }
}

async function saveFlow() {
  saving.value = true;
  savedMsg.value = "";
  try {
    const updated = await api.patch<Flow>(`/v1/flows/${flowId.value}`, {
      name: flowName.value || "Untitled flow",
      is_active: isActive.value,
      is_outgoing_end: isOutgoingEnd.value,
      definition: buildIVRFlowData(),
    });
    syncRenderedAudio(updated.definition); // reflect server-rendered TTS audio at once
    savedMsg.value = "Saved";
    setTimeout(() => (savedMsg.value = ""), 1500);
  } catch (e) {
    savedMsg.value = errMessage(e, "save failed");
  } finally {
    saving.value = false;
  }
}

onMounted(async () => {
  try {
    const [f, list, agents, teams] = await Promise.all([
      api.get<Flow>(`/v1/flows/${flowId.value}`),
      api.get<Paginated<Flow>>("/v1/flows"),
      api.get<Agent[]>("/v1/agents"),
      api.get<Team[]>("/v1/teams"),
    ]);
    availableAgents.value = agents;
    availableTeams.value = teams;
    flowName.value = f.name;
    isActive.value = f.is_active;
    isOutgoingEnd.value = f.is_outgoing_end;
    if (f.definition && f.definition.version === 2 && f.definition.nodes?.length) {
      loadFromIVRFlowData(f.definition);
    } else {
      // Empty flow — seed with just the Start node so the canvas isn't
      // blank and the entry-point invariant always holds.
      ensureStartNode();
    }
    availableFlows.value = list.items;
  } catch (e) {
    savedMsg.value = errMessage(e, "load failed");
  }
});
</script>

<template>
  <div class="fe-root">
    <!-- Toolbar -->
    <div class="fe-toolbar">
      <IconButton title="Back to flows" @click="router.push({ name: 'flows' })">
        <ArrowLeft :size="16" />
      </IconButton>
      <input v-model="flowName" placeholder="Flow name" class="fe-name-input" />
      <Toggle v-model="isActive" label="Active" />
      <Toggle v-model="isOutgoingEnd" label="Outgoing Post-Call" :disabled="!isActive" />
      <div class="fe-spacer" />
      <span v-if="savedMsg" class="fe-saved-msg">{{ savedMsg }}</span>
      <Button variant="secondary" size="sm" @click="showSimPanel = !showSimPanel">
        <PlayCircle :size="16" />
        Test
      </Button>
      <Button size="sm" :loading="saving" @click="saveFlow">
        <Save :size="16" />
        {{ saving ? "Saving…" : "Save" }}
      </Button>
    </div>

    <!-- Palette -->
    <div class="fe-palette">
      <button
        v-for="p in palette"
        :key="p.type"
        type="button"
        class="outline small fe-palette-btn"
        @click="addNodeFromPalette(p.type)"
      >
        <span class="fe-palette-dot" :class="p.color" />
        <component :is="p.icon" :size="12" />
        {{ p.label }}
      </button>
    </div>

    <!-- Main: canvas + right panel -->
    <div class="fe-main">
      <div class="fe-canvas">
        <VueFlow
          :nodes="nodes"
          :edges="edges"
          :node-types="nodeTypes"
          :default-edge-options="{
            type: 'default',
            animated: true,
            markerEnd: MarkerType.ArrowClosed,
          }"
          :is-valid-connection="isValidConnection"
          fit-view-on-init
          @node-click="onNodeClick"
          @pane-click="onPaneClick"
        >
          <Background :pattern-color="bgPattern" :gap="20" />
          <Controls />
          <MiniMap />
        </VueFlow>
      </div>

      <aside v-if="!showSimPanel" class="fe-panel">
        <IVRNodeProperties
          v-if="selectedNode"
          :key="selectedNode.id + '|' + ((selectedNode.data?.config?.audio_file as string) || '')"
          :node="selectedNode as any"
          :current-flow-id="flowId"
          :available-flows="availableFlows"
          :available-agents="availableAgents"
          :available-teams="availableTeams"
          @update:node="onUpdateNode"
          @delete="requestDeleteSelectedNode"
        />
        <div v-else class="fe-panel-empty text-lighter">
          Click a node to edit, or pick one from the palette above to add it.
        </div>
      </aside>

      <!-- Simulator panel — opens in place of the properties panel
           when the operator clicks "Test". POSTs to /v1/flows/{id}/simulate
           and renders the returned step trace. No FreeSWITCH involved;
           audio "plays" and HTTP "calls" are recorded as intent only. -->
      <aside v-else class="fe-panel fe-sim-panel">
        <div class="fe-sim-head">
          <div class="hstack gap-2">
            <PlayCircle :size="16" class="fe-sim-head-icon" />
            <span class="fe-sim-head-title">Simulator</span>
          </div>
          <button type="button" class="ghost small" @click="showSimPanel = false">Close</button>
        </div>
        <div class="fe-sim-form">
          <label class="fe-sim-caller-label">
            Caller
            <input
              v-model="simCaller"
              :disabled="simSession?.status === 'awaiting_dtmf'"
              class="mono"
            />
          </label>
          <Button block :loading="simBusy" @click="startSimulation">
            {{ simSession ? "Restart simulation" : "Start simulation" }}
          </Button>
          <p v-if="simError" class="fe-sim-error">{{ simError }}</p>
        </div>

        <div class="fe-sim-body">
          <div v-if="!simSession" class="fe-sim-empty text-lighter">
            Start a simulation. The walker pauses on every Menu / Gather and
            asks for the next digit, just like a real call.
          </div>
          <div v-else>
            <ol class="fe-sim-steps">
              <li v-for="(step, idx) in simSession.steps" :key="idx" class="fe-sim-step">
                <div class="hstack gap-1">
                  <span class="mono fe-sim-step-idx text-lighter">{{ idx + 1 }}</span>
                  <span class="fe-sim-step-type">{{ step.node_type }}</span>
                  <span class="text-lighter">·</span>
                  <span class="text-light">{{ step.action }}</span>
                </div>
                <div v-if="step.detail" class="fe-sim-step-detail text-light">
                  {{ step.detail }}
                </div>
                <div v-if="step.dtmf_input" class="mono fe-sim-step-detail text-light">
                  input: {{ step.dtmf_input }}
                </div>
                <div v-if="step.outcome" class="fe-sim-step-outcome text-lighter">
                  → {{ step.outcome }}
                </div>
              </li>
            </ol>

            <!-- Live prompt for the paused node — Menu/Gather only. -->
            <div
              v-if="simSession.status === 'awaiting_dtmf' && simSession.awaiting_node"
              class="fe-sim-waiting"
            >
              <div class="fe-sim-waiting-label text-light">
                Waiting at {{ simSession.awaiting_node.node_type }}
              </div>
              <div v-if="simSession.awaiting_node.prompt" class="fe-sim-waiting-prompt">
                {{ simSession.awaiting_node.prompt }}
              </div>
              <form class="hstack gap-2" @submit.prevent="sendSimInput">
                <input
                  v-model="simInput"
                  autofocus
                  placeholder="press a digit"
                  class="mono fe-sim-input"
                />
                <Button type="submit" size="sm" :disabled="simBusy || !simInput">Send</Button>
              </form>
              <div class="fe-sim-hint text-lighter">
                For Menu: a single digit (1-9, *, #). For Gather: the full sequence.
              </div>
            </div>

            <!-- Ended state — flow finished or errored. -->
            <div
              v-else-if="simSession.status === 'ended' || simSession.status === 'error'"
              class="fe-sim-ended text-light"
            >
              <div><span class="fe-sim-ended-label">Ended at:</span> {{ simSession.ended_at }}</div>
              <div>
                <span class="fe-sim-ended-label">Hangup cause:</span> {{ simSession.hangup_cause }}
              </div>
              <div v-if="Object.keys(simSession.vars).length">
                <span class="fe-sim-ended-label">Vars:</span> {{ JSON.stringify(simSession.vars) }}
              </div>
              <div v-if="simSession.error" style="color: var(--danger)">
                <span class="fe-sim-ended-label">Error:</span> {{ simSession.error }}
              </div>
            </div>
          </div>
        </div>
      </aside>
    </div>
  </div>
</template>

<style scoped>
/* Structural chrome around the vue-flow canvas — toolbar, palette, side
   panels, simulator drawer. Oat has no app-shell/toolbar primitive.
   Colours all via tokens; palette dot colours are node-type identity
   (same Tailwind-named keys the already-migrated node components key
   their gradients off — see components/calling/nodes/BaseNode.vue). */
.fe-root { height: 100vh; display: flex; flex-direction: column; background: var(--background); color: var(--foreground); }

.fe-toolbar {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.5rem 1rem;
  border-bottom: 1px solid var(--border);
  background: var(--card);
  flex-shrink: 0;
}
.fe-name-input { max-width: 280px; }
.fe-spacer { flex: 1 1 auto; }
.fe-saved-msg { font-size: var(--text-8); color: var(--success); }

.fe-palette {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.5rem 1rem;
  border-bottom: 1px solid var(--border);
  background: color-mix(in srgb, var(--card) 60%, transparent);
  overflow-x: auto;
  flex-shrink: 0;
}
.fe-palette-btn { flex-shrink: 0; }
.fe-palette-dot { display: inline-block; width: 0.5rem; height: 0.5rem; border-radius: var(--radius-full); }

.fe-main { flex: 1 1 auto; display: flex; overflow: hidden; }
.fe-canvas { flex: 1 1 auto; position: relative; background: var(--background); }

.fe-panel {
  width: 420px;
  min-width: 0;
  border-left: 1px solid var(--border);
  background: var(--card);
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.fe-sim-panel { width: 480px; }
.fe-panel-empty { padding: 1.5rem; font-size: var(--text-7); text-align: center; }

.fe-sim-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.75rem 1rem;
  border-bottom: 1px solid var(--border);
}
.fe-sim-head-icon { color: var(--primary); }
.fe-sim-head-title { font-size: var(--text-7); font-weight: 500; }

.fe-sim-form {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  padding: 1rem;
  border-bottom: 1px solid var(--border);
}
.fe-sim-caller-label { font-size: var(--text-8); color: var(--muted-foreground); }
.fe-sim-error { font-size: var(--text-8); color: var(--danger); }

.fe-sim-body { flex: 1 1 auto; overflow: auto; padding: 1rem; font-size: var(--text-8); }
.fe-sim-empty { text-align: center; padding: 2rem 0; }
.fe-sim-steps { display: flex; flex-direction: column; gap: 0.5rem; list-style: none; margin: 0; padding: 0; }
.fe-sim-step {
  border: 1px solid var(--border);
  border-radius: var(--radius-medium);
  padding: 0.5rem;
  background: color-mix(in srgb, var(--secondary) 40%, transparent);
}
.fe-sim-step-idx { font-size: 10px; }
.fe-sim-step-type { font-weight: 500; }
.fe-sim-step-detail { margin-top: 0.25rem; overflow-wrap: break-word; }
.fe-sim-step-outcome { margin-top: 0.25rem; font-size: 10px; }

.fe-sim-waiting {
  margin-top: 1rem;
  border: 1px solid color-mix(in srgb, var(--primary) 50%, transparent);
  border-radius: var(--radius-medium);
  padding: 0.75rem;
  background: color-mix(in srgb, var(--primary) 8%, transparent);
}
.fe-sim-waiting-label { font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; margin-bottom: 0.25rem; }
.fe-sim-waiting-prompt { margin-bottom: 0.5rem; }
.fe-sim-input { flex: 1 1 auto; }
.fe-sim-hint { margin-top: 0.25rem; font-size: 10px; }

.fe-sim-ended {
  margin-top: 0.75rem;
  padding: 0.5rem;
  border-radius: var(--radius-medium);
  border: 1px solid var(--border);
  background: color-mix(in srgb, var(--secondary) 40%, transparent);
}
.fe-sim-ended-label { font-weight: 500; color: var(--foreground); }
</style>

<style>
.vue-flow__node {
  font-family: inherit;
}

/* Dark-theme the vue-flow chrome — its bundled theme-default.css assumes a
   light canvas, so controls/minimap render as white boxes otherwise. */
.vue-flow__controls {
  box-shadow: 0 1px 3px rgb(0 0 0 / 0.4);
}
.vue-flow__controls-button {
  background: var(--color-surface-2);
  border-bottom: 1px solid var(--color-border);
  fill: var(--color-ink-muted);
}
.vue-flow__controls-button:hover {
  background: var(--color-surface);
  fill: var(--color-ink);
}
.vue-flow__minimap {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.vue-flow__minimap-mask {
  fill: color-mix(in srgb, var(--color-overlay) 60%, transparent);
}
.vue-flow__edge-path {
  stroke: var(--color-border-strong);
}
.vue-flow__edge.animated .vue-flow__edge-path,
.vue-flow__edge.selected .vue-flow__edge-path {
  stroke: var(--color-accent);
}
.vue-flow__attribution {
  display: none;
}
</style>
