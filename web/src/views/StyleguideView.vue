<script setup lang="ts">
import { ref } from "vue";
import { Plus, Trash2, Phone } from "lucide-vue-next";
import {
  Badge,
  Button,
  Card,
  CopyButton,
  DataTable,
  type Column,
  EmptyState,
  IconButton,
  LevelMeter,
  Modal,
  Panel,
  Select,
  SignalDot,
  Tabs,
  Textarea,
  TextField,
  Toggle,
  Tooltip,
} from "../components/ui";
import { toast } from "../lib/toast";

const text = ref("");
const area = ref("");
const sel = ref("exotel");
const on = ref(true);
const modalOpen = ref(false);
const tab = ref("one");

const tokens = [
  "base",
  "surface",
  "surface-2",
  "border",
  "ink",
  "ink-muted",
  "ink-faint",
  "accent",
  "live",
  "success",
  "danger",
  "warning",
];

const cols: Column[] = [
  { key: "from", label: "From", mono: true },
  { key: "to", label: "To" },
  { key: "status", label: "Status" },
  { key: "dur", label: "Duration", mono: true, align: "right" },
];
const rows = [
  { id: 1, from: "+91 98765 43210", to: "agent.rao", status: "on_call", dur: "02:14" },
  { id: 2, from: "+91 90000 11111", to: "support", status: "ended", dur: "00:48" },
];
</script>

<template>
  <div class="min-h-screen bg-base text-ink p-10">
    <div class="max-w-5xl mx-auto space-y-12">
      <header>
        <h1 class="text-xl font-semibold tracking-tight">Operator styleguide</h1>
        <p class="text-sm text-ink-muted mt-1">
          Every primitive, in every state. Dev-only — built to design views against.
        </p>
      </header>

      <!-- Tokens -->
      <section class="space-y-3">
        <h2 class="text-sm font-medium text-ink-muted uppercase tracking-wide">Color tokens</h2>
        <div class="grid grid-cols-3 sm:grid-cols-6 gap-3">
          <div v-for="t in tokens" :key="t" class="space-y-1.5">
            <div
              class="h-12 rounded-lg border border-border"
              :style="{ background: `var(--color-${t})` }"
            ></div>
            <div class="text-xs font-mono text-ink-faint">{{ t }}</div>
          </div>
        </div>
      </section>

      <!-- Type -->
      <section class="space-y-2">
        <h2 class="text-sm font-medium text-ink-muted uppercase tracking-wide">Type</h2>
        <p class="text-xl font-semibold tracking-tight">Page title — text-xl semibold</p>
        <p class="text-sm">Body — the quick brown fox jumps over the lazy dog.</p>
        <p class="text-xs text-ink-muted">Meta — secondary caption text.</p>
        <p class="font-mono text-sm tabular-nums">Mono data — +91 98765 43210 · DTMF 1234 · 02:14</p>
      </section>

      <!-- Buttons -->
      <section class="space-y-3">
        <h2 class="text-sm font-medium text-ink-muted uppercase tracking-wide">Buttons</h2>
        <div class="flex flex-wrap items-center gap-3">
          <Button variant="primary">Primary</Button>
          <Button variant="secondary">Secondary</Button>
          <Button variant="ghost">Ghost</Button>
          <Button variant="danger">Danger</Button>
          <Button variant="primary" loading>Loading</Button>
          <Button variant="primary" disabled>Disabled</Button>
          <Button variant="secondary" size="sm">Small</Button>
          <Button variant="primary"><Phone class="h-4 w-4" />With icon</Button>
        </div>
        <div class="flex items-center gap-3">
          <IconButton title="Add"><Plus class="h-4 w-4" /></IconButton>
          <IconButton title="Delete" variant="danger"><Trash2 class="h-4 w-4" /></IconButton>
          <IconButton title="Secondary" variant="secondary"><Plus class="h-4 w-4" /></IconButton>
          <Tooltip content="A helpful hint">
            <IconButton title="Hover me"><Phone class="h-4 w-4" /></IconButton>
          </Tooltip>
        </div>
      </section>

      <!-- Form -->
      <section class="space-y-3">
        <h2 class="text-sm font-medium text-ink-muted uppercase tracking-wide">Form fields</h2>
        <div class="grid sm:grid-cols-2 gap-4 max-w-2xl">
          <TextField v-model="text" label="Display name" placeholder="Jane Rao" hint="Shown to callers" />
          <TextField v-model="text" label="SIP username" mono placeholder="agent.rao" />
          <TextField v-model="text" label="With error" error="This field is required" />
          <Select
            v-model="sel"
            label="Provider"
            :options="[
              { value: 'exotel', label: 'Exotel' },
              { value: 'airtel', label: 'Airtel' },
            ]"
          />
          <Textarea v-model="area" label="Notes" placeholder="Anything…" />
          <div class="flex items-end"><Toggle v-model="on" label="Record calls" /></div>
        </div>
      </section>

      <!-- Status -->
      <section class="space-y-3">
        <h2 class="text-sm font-medium text-ink-muted uppercase tracking-wide">Status</h2>
        <div class="flex flex-wrap items-center gap-4">
          <SignalDot state="online" />
          <SignalDot state="on_call" />
          <SignalDot state="ringing" />
          <SignalDot state="away" />
          <SignalDot state="offline" />
          <LevelMeter active />
          <LevelMeter />
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <Badge tone="neutral">neutral</Badge>
          <Badge tone="success">success</Badge>
          <Badge tone="live">live</Badge>
          <Badge tone="warning">warning</Badge>
          <Badge tone="danger">danger</Badge>
          <Badge tone="accent">accent</Badge>
        </div>
      </section>

      <!-- Table -->
      <section class="space-y-3">
        <h2 class="text-sm font-medium text-ink-muted uppercase tracking-wide">DataTable</h2>
        <Panel>
          <template #title>Recent calls</template>
          <template #actions><Button size="sm" variant="secondary">Export</Button></template>
          <DataTable :columns="cols" :rows="rows">
            <template #cell-status="{ value }">
              <SignalDot :state="value === 'on_call' ? 'on_call' : 'offline'" :label="String(value)" />
            </template>
            <template #row-actions>
              <IconButton title="Delete" size="sm" variant="danger"><Trash2 class="h-4 w-4" /></IconButton>
            </template>
          </DataTable>
        </Panel>
        <Card padded>
          <EmptyState :icon="Phone" title="No calls yet" hint="Calls will appear here once your agents start dialing.">
            <template #action><Button>Place a call</Button></template>
          </EmptyState>
        </Card>
      </section>

      <!-- Overlays + feedback -->
      <section class="space-y-3">
        <h2 class="text-sm font-medium text-ink-muted uppercase tracking-wide">Overlays & feedback</h2>
        <div class="flex flex-wrap items-center gap-3">
          <Button @click="modalOpen = true">Open modal</Button>
          <Button variant="secondary" @click="toast.success('Saved changes')">Toast success</Button>
          <Button variant="secondary" @click="toast.error('Something went wrong')">Toast error</Button>
          <Button variant="secondary" @click="toast.info('Heads up')">Toast info</Button>
          <CopyButton value="tk_live_example" label="Copy key" />
        </div>
        <Tabs v-model="tab" :tabs="[{ value: 'one', label: 'Overview' }, { value: 'two', label: 'Advanced' }]" />
        <p class="text-sm text-ink-muted">Active tab: {{ tab }}</p>
      </section>
    </div>

    <Modal v-model:open="modalOpen" title="Example dialog" size="md">
      <div class="space-y-4">
        <TextField label="Name" placeholder="Type something…" />
        <p class="text-sm text-ink-muted">Esc closes, focus is trapped, scrim click dismisses.</p>
      </div>
      <template #footer>
        <Button variant="ghost" @click="modalOpen = false">Cancel</Button>
        <Button @click="modalOpen = false">Save</Button>
      </template>
    </Modal>
  </div>
</template>
