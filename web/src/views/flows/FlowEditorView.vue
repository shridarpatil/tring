<script setup lang="ts">
// FlowEditorView — the IVR flow editor. Combines what Whatomate splits
// across FlowBuilder + IVRFlowEditorView into one Tring view:
//   • Top toolbar: name input, active/call-start toggles, save button
//   • Palette: 8 node types (click to add at viewport center)
//   • Main: vue-flow canvas (drag/connect) + properties panel on the right
// Uses localStorage as the persistence stub until the backend lands.
import { ref, computed, onMounted, markRaw, watch } from "vue";
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
} from "lucide-vue-next";

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

import type { Flow, IVRFlowData, IVRNodeType } from "../../api/types";

const route = useRoute();
const router = useRouter();
const flowId = computed(() => route.params.id as string);

const flowName = ref("");
const isActive = ref(true);
const isCallStart = ref(false);
const isOutgoingEnd = ref(false);
const saving = ref(false);
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
    max_digits: 10,
    terminator: "#",
    timeout_seconds: 10,
    max_retries: 3,
    store_as: "",
  },
  http_callback: { url: "", method: "GET", headers: {}, body_template: "", timeout_seconds: 10 },
  transfer: { agent_number: "", ring_timeout: 30 },
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

async function saveFlow() {
  saving.value = true;
  savedMsg.value = "";
  try {
    await api.patch<Flow>(`/v1/flows/${flowId.value}`, {
      name: flowName.value || "Untitled flow",
      is_active: isActive.value,
      is_call_start: isCallStart.value,
      is_outgoing_end: isOutgoingEnd.value,
      definition: buildIVRFlowData(),
    });
    savedMsg.value = "Saved";
    setTimeout(() => (savedMsg.value = ""), 1500);
  } catch (e) {
    savedMsg.value = e instanceof Error ? e.message : "save failed";
  } finally {
    saving.value = false;
  }
}

onMounted(async () => {
  try {
    const [f, list] = await Promise.all([
      api.get<Flow>(`/v1/flows/${flowId.value}`),
      api.get<Flow[]>("/v1/flows"),
    ]);
    flowName.value = f.name;
    isActive.value = f.is_active;
    isCallStart.value = f.is_call_start;
    isOutgoingEnd.value = f.is_outgoing_end;
    if (f.definition && f.definition.version === 2 && f.definition.nodes?.length) {
      loadFromIVRFlowData(f.definition);
    } else {
      // Empty flow — seed with just the Start node so the canvas isn't
      // blank and the entry-point invariant always holds.
      ensureStartNode();
    }
    availableFlows.value = list;
  } catch (e) {
    savedMsg.value = e instanceof Error ? e.message : "load failed";
  }
});
</script>

<template>
  <div class="h-screen flex flex-col bg-gray-50">
    <!-- Toolbar -->
    <div class="flex items-center gap-3 px-4 py-2 border-b border-gray-200 bg-white shrink-0">
      <button
        class="h-8 w-8 rounded-md hover:bg-gray-100 flex items-center justify-center"
        title="Back to flows"
        @click="router.push({ name: 'flows' })"
      >
        <ArrowLeft class="h-4 w-4" />
      </button>
      <input
        v-model="flowName"
        placeholder="Flow Name"
        class="h-8 text-sm rounded-md border border-gray-300 px-3 max-w-[280px] flex-1 sm:flex-none"
      />
      <label class="flex items-center gap-2 ml-2">
        <input v-model="isActive" type="checkbox" class="rounded" />
        <span class="text-xs whitespace-nowrap">Active</span>
      </label>
      <label class="flex items-center gap-2 ml-1">
        <input
          v-model="isCallStart"
          type="checkbox"
          :disabled="!isActive"
          class="rounded"
        />
        <span class="text-xs whitespace-nowrap">Incoming Call Start</span>
      </label>
      <label class="flex items-center gap-2 ml-1">
        <input
          v-model="isOutgoingEnd"
          type="checkbox"
          :disabled="!isActive"
          class="rounded"
        />
        <span class="text-xs whitespace-nowrap">Outgoing Post-Call</span>
      </label>
      <div class="flex-1" />
      <span v-if="savedMsg" class="text-xs text-green-600">{{ savedMsg }}</span>
      <button
        :disabled="saving"
        class="bg-gray-900 text-white text-sm rounded-md px-3 py-1.5 hover:bg-gray-800 disabled:opacity-50 flex items-center gap-1"
        @click="saveFlow"
      >
        <Save class="h-4 w-4" />
        {{ saving ? "Saving…" : "Save" }}
      </button>
    </div>

    <!-- Palette -->
    <div
      class="flex items-center gap-2 px-4 py-2 border-b border-gray-200 bg-gray-50 overflow-x-auto shrink-0"
    >
      <button
        v-for="p in palette"
        :key="p.type"
        class="h-7 text-xs rounded-md border border-gray-300 bg-white px-2 hover:bg-gray-50 flex items-center gap-1.5 shrink-0"
        @click="addNodeFromPalette(p.type)"
      >
        <span :class="['w-2 h-2 rounded-full', p.color]" />
        <component :is="p.icon" class="w-3 h-3" />
        {{ p.label }}
      </button>
    </div>

    <!-- Main: canvas + right panel -->
    <div class="flex-1 flex overflow-hidden">
      <div class="flex-1 relative bg-white">
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
          <Background pattern-color="#e5e7eb" :gap="20" />
          <Controls />
          <MiniMap />
        </VueFlow>
      </div>

      <aside
        class="w-[420px] min-w-0 border-l border-gray-200 bg-white shrink-0 flex flex-col overflow-hidden"
      >
        <IVRNodeProperties
          v-if="selectedNode"
          :node="selectedNode as any"
          :current-flow-id="flowId"
          :available-flows="availableFlows"
          @update:node="onUpdateNode"
          @delete="requestDeleteSelectedNode"
        />
        <div v-else class="p-6 text-sm text-gray-400 text-center">
          Click a node to edit, or pick one from the palette above to add it.
        </div>
      </aside>
    </div>
  </div>
</template>

<style>
.vue-flow__node {
  font-family: inherit;
}
</style>
