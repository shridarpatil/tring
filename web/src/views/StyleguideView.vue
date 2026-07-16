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
  <div class="styleguide">
    <div class="styleguide-inner">
      <header class="vstack" style="gap: 0.25rem">
        <h1 class="page-title">Operator styleguide</h1>
        <p class="text-light">
          Every primitive, in every state. Dev-only — built to design views against.
        </p>
      </header>

      <!-- Tokens -->
      <section class="vstack">
        <h2 class="section-title">Color tokens</h2>
        <div class="token-grid">
          <div v-for="t in tokens" :key="t" class="vstack" style="gap: 0.375rem">
            <div class="token-swatch" :style="{ background: `var(--color-${t})` }"></div>
            <div class="text-lighter mono">{{ t }}</div>
          </div>
        </div>
      </section>

      <!-- Type -->
      <section class="vstack" style="gap: 0.5rem">
        <h2 class="section-title">Type</h2>
        <p class="page-title">Page title — text-xl semibold</p>
        <p>Body — the quick brown fox jumps over the lazy dog.</p>
        <p class="text-light">Meta — secondary caption text.</p>
        <p class="mono" style="font-variant-numeric: tabular-nums">Mono data — +91 98765 43210 · DTMF 1234 · 02:14</p>
      </section>

      <!-- Buttons -->
      <section class="vstack">
        <h2 class="section-title">Buttons</h2>
        <div class="hstack">
          <Button variant="primary">Primary</Button>
          <Button variant="secondary">Secondary</Button>
          <Button variant="ghost">Ghost</Button>
          <Button variant="danger">Danger</Button>
          <Button variant="primary" loading>Loading</Button>
          <Button variant="primary" disabled>Disabled</Button>
          <Button variant="secondary" size="sm">Small</Button>
          <Button variant="primary"><Phone :size="16" />With icon</Button>
        </div>
        <div class="hstack">
          <IconButton title="Add"><Plus :size="16" /></IconButton>
          <IconButton title="Delete" variant="danger"><Trash2 :size="16" /></IconButton>
          <IconButton title="Secondary" variant="secondary"><Plus :size="16" /></IconButton>
          <Tooltip content="A helpful hint">
            <IconButton title="Hover me"><Phone :size="16" /></IconButton>
          </Tooltip>
        </div>
      </section>

      <!-- Form -->
      <section class="vstack">
        <h2 class="section-title">Form fields</h2>
        <div class="grid grid-2" style="max-width: 42rem">
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
          <div style="display: flex; align-items: flex-end"><Toggle v-model="on" label="Record calls" /></div>
        </div>
      </section>

      <!-- Status -->
      <section class="vstack">
        <h2 class="section-title">Status</h2>
        <div class="hstack" style="gap: 1rem">
          <SignalDot state="online" />
          <SignalDot state="on_call" />
          <SignalDot state="ringing" />
          <SignalDot state="away" />
          <SignalDot state="offline" />
          <LevelMeter active />
          <LevelMeter />
        </div>
        <div class="hstack" style="gap: 0.5rem">
          <Badge tone="neutral">neutral</Badge>
          <Badge tone="success">success</Badge>
          <Badge tone="live">live</Badge>
          <Badge tone="warning">warning</Badge>
          <Badge tone="danger">danger</Badge>
          <Badge tone="accent">accent</Badge>
        </div>
      </section>

      <!-- Table -->
      <section class="vstack">
        <h2 class="section-title">DataTable</h2>
        <Panel>
          <template #title>Recent calls</template>
          <template #actions><Button size="sm" variant="secondary">Export</Button></template>
          <DataTable :columns="cols" :rows="rows">
            <template #cell-status="{ value }">
              <SignalDot :state="value === 'on_call' ? 'on_call' : 'offline'" :label="String(value)" />
            </template>
            <template #row-actions>
              <IconButton title="Delete" size="sm" variant="danger"><Trash2 :size="16" /></IconButton>
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
      <section class="vstack">
        <h2 class="section-title">Overlays &amp; feedback</h2>
        <div class="hstack">
          <Button @click="modalOpen = true">Open modal</Button>
          <Button variant="secondary" @click="toast.success('Saved changes')">Toast success</Button>
          <Button variant="secondary" @click="toast.error('Something went wrong')">Toast error</Button>
          <Button variant="secondary" @click="toast.info('Heads up')">Toast info</Button>
          <CopyButton value="tk_live_example" label="Copy key" />
        </div>
        <Tabs v-model="tab" :tabs="[{ value: 'one', label: 'Overview' }, { value: 'two', label: 'Advanced' }]" />
        <p class="text-light">Active tab: {{ tab }}</p>
      </section>
    </div>

    <Modal v-model:open="modalOpen" title="Example dialog" size="md">
      <div class="vstack">
        <TextField label="Name" placeholder="Type something…" />
        <p class="text-light">Esc closes, focus is trapped, scrim click dismisses.</p>
      </div>
      <template #footer>
        <Button variant="ghost" @click="modalOpen = false">Cancel</Button>
        <Button @click="modalOpen = false">Save</Button>
      </template>
    </Modal>
  </div>
</template>

<style scoped>
/* Standalone dev-only route (outside AppLayout), so it needs its own
   full-page background — same pattern as LoginView. Structural only;
   colours via tokens. */
.styleguide { min-height: 100vh; background: var(--background); color: var(--foreground); padding: 2.5rem; }
.styleguide-inner { max-width: 64rem; margin-inline: auto; display: flex; flex-direction: column; gap: 3rem; }
.section-title {
  font-size: var(--text-8);
  font-weight: 500;
  color: var(--muted-foreground);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.token-grid { display: grid; grid-template-columns: repeat(6, 1fr); gap: 0.75rem; }
@media (max-width: 820px) {
  .token-grid { grid-template-columns: repeat(3, 1fr); }
}
.token-swatch { height: 3rem; border-radius: var(--radius-large); border: 1px solid var(--border); }
</style>
