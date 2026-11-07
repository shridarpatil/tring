<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import { useRoute, useRouter } from "vue-router";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import { errMessage } from "../lib/errors";
import { toast } from "../lib/toast";
import { confirmDialog } from "../lib/confirm";
import { callStatusTone } from "../lib/status";
import { fmtTalk, memberTone } from "../lib/me";
import { SupervisorSession, type SuperviseMode } from "../lib/supervisor";
import type { Call } from "../api/types";
import { Badge, Button, Select } from "../components/ui";

interface Member {
  user_id: string;
  sip_username: string;
  display_name?: string;
  is_manager: boolean;
  status: string;
}
interface Team {
  name: string;
  is_manager: boolean;
  queue: { waiting: number };
  members: Member[];
  stats?: { offered: number; answered: number; abandoned: number; avg_wait_seconds: number };
  calls?: Call[];
}
interface Candidate {
  id: string;
  display_name?: string;
  sip_username: string;
}

const route = useRoute();
const router = useRouter();
const name = computed(() => String(route.params.name));
const seesAll = computed(() => auth.seesAllTeams());

const team = ref<Team | null>(null);
const notFound = ref(false);
const teamNames = ref<string[]>([]);
// Guards async callbacks that resolve after the view (or the team) is gone.
let unmounted = false;
let poll: ReturnType<typeof setInterval> | undefined;

async function load() {
  const requested = name.value;
  try {
    const t = await api.get<Team>(`/v1/my-teams/${encodeURIComponent(requested)}`);
    if (unmounted || requested !== name.value) return;
    team.value = t;
    notFound.value = false;
  } catch (e) {
    if (unmounted || requested !== name.value) return;
    // 404 also covers "not yours": the backend hides team names.
    if ((e as { status?: number })?.status === 404) notFound.value = true;
  }
}

const manages = computed(() => !!team.value?.stats);
const liveCalls = computed(() =>
  (team.value?.calls ?? []).filter((c) => c.status === "ringing" || c.status === "answered"),
);

// --- members ---------------------------------------------------------------
const candidates = ref<Candidate[]>([]);
const pick = ref("");
async function loadCandidates() {
  if (!manages.value) return;
  const requested = name.value;
  try {
    const list = await api.get<Candidate[]>(`/v1/my-teams/${encodeURIComponent(requested)}/candidates`);
    if (!unmounted && requested === name.value) candidates.value = list;
  } catch {
    /* picker stays as it was */
  }
}
const candidateOptions = computed(() =>
  candidates.value.map((c) => ({ value: c.id, label: c.display_name || c.sip_username })),
);

async function addMember() {
  if (!pick.value) return;
  try {
    await api.post(`/v1/my-teams/${encodeURIComponent(name.value)}/members`, { user_id: pick.value });
    pick.value = "";
    await Promise.all([load(), loadCandidates()]);
  } catch (e) {
    toast.error(errMessage(e, "could not add member"));
  }
}
async function removeMember(m: Member) {
  if (!(await confirmDialog(`Remove ${m.display_name || m.sip_username} from ${name.value}?`))) return;
  try {
    await api.del(`/v1/my-teams/${encodeURIComponent(name.value)}/members/${m.user_id}`);
    await Promise.all([load(), loadCandidates()]);
  } catch (e) {
    toast.error(errMessage(e, "could not remove member"));
  }
}
async function setManager(m: Member, v: boolean) {
  try {
    await api.put(`/v1/my-teams/${encodeURIComponent(name.value)}/members/${m.user_id}/manager`, { is_manager: v });
    await load();
  } catch (e) {
    toast.error(errMessage(e, "could not change manager flag"));
  }
}

// --- supervise (one session at a time, like the dashboard) ------------------
const active = ref<{ callId: string; mode: SuperviseMode; session: SupervisorSession } | null>(null);
const LABELS: Record<SuperviseMode, string> = { monitor: "Monitor", whisper: "Whisper", barge: "Barge" };

// Bumped by every start/stop/reset/unmount so a connect that resolves after
// any of them hangs its own session up instead of orphaning it.
let superviseGen = 0;
// The session currently connecting, so stopSupervise can cancel it.
let pending: SupervisorSession | undefined;

async function startSupervise(c: Call) {
  stopSupervise();
  const gen = superviseGen;
  let session: SupervisorSession | undefined;
  try {
    const creds = await api.supervise(c.id);
    if (gen !== superviseGen || unmounted) return;
    session = new SupervisorSession(creds.gateway_url, creds.room, creds.token, creds.ice_servers || []);
    pending = session;
    session.onClose = (reason) => {
      if (active.value?.session === session) active.value = null;
      if (reason) toast.error(reason);
    };
    await session.connect("monitor");
    if (pending === session) pending = undefined;
    if (gen !== superviseGen || unmounted) {
      session.hangup();
      return;
    }
    active.value = { callId: c.id, mode: "monitor", session };
  } catch (e) {
    // A failed connect leaves a half-open session: hang it up so it can't
    // linger in the room, and stay quiet if the user has moved on.
    if (session) {
      session.hangup();
      if (pending === session) pending = undefined;
    }
    if (gen !== superviseGen || unmounted) return;
    toast.error(errMessage(e, "Could not start supervising this call"));
  }
}
function setMode(mode: SuperviseMode) {
  if (!active.value) return;
  active.value.session.setMode(mode);
  active.value.mode = mode;
}
function stopSupervise() {
  superviseGen++;
  pending?.hangup();
  pending = undefined;
  active.value?.session.hangup();
  active.value = null;
}

// --- recordings (Bearer blob fetch) -----------------------------------------
const recUrl = ref<Record<string, string>>({});
async function playRecording(c: Call) {
  if (!c.recording_url || recUrl.value[c.id]) return;
  const token = auth.token();
  const res = await fetch(c.recording_url, { headers: token ? { Authorization: `Bearer ${token}` } : {} });
  if (!res.ok) {
    toast.error("recording not available yet");
    return;
  }
  const url = URL.createObjectURL(await res.blob());
  if (unmounted) {
    URL.revokeObjectURL(url);
    return;
  }
  recUrl.value = { ...recUrl.value, [c.id]: url };
}
function revokeRecordings() {
  Object.values(recUrl.value).forEach((u) => URL.revokeObjectURL(u));
  recUrl.value = {};
}

function fmt(d?: string) {
  return d ? new Date(d).toLocaleString() : "—";
}
function who(c: Call) {
  return c.client_ref || c.from_number;
}

// --- lifecycle ---------------------------------------------------------------
function reset() {
  stopSupervise();
  revokeRecordings();
  team.value = null;
  notFound.value = false;
  candidates.value = [];
  pick.value = "";
  load().then(loadCandidates);
}
watch(name, reset);

function goTeam(v: string) {
  if (v && v !== name.value) router.push({ name: "my-team", params: { name: v } });
}

onMounted(async () => {
  api
    .get<{ name: string }[]>("/v1/my-teams")
    .then((l) => !unmounted && (teamNames.value = l.map((t) => t.name)))
    .catch(() => {});
  await load();
  if (unmounted) return;
  loadCandidates();
  poll = setInterval(load, 5000);
});
onUnmounted(() => {
  unmounted = true;
  clearInterval(poll);
  stopSupervise();
  revokeRecordings();
});
</script>

<template>
  <div class="view">
    <div class="row-between">
      <h1 class="page-title">{{ name }}</h1>
      <div v-if="seesAll && teamNames.length > 1" style="min-width: 12rem">
        <Select
          :model-value="name"
          :options="teamNames.map((n) => ({ value: n, label: n }))"
          @update:model-value="goTeam"
        />
      </div>
    </div>

    <p v-if="notFound" class="text-lighter">Team not found.</p>
    <p v-else-if="!team" class="text-lighter">Loading…</p>

    <template v-else>
      <div v-if="team.stats" class="card vstack">
        <h2>Today</h2>
        <div class="hstack">
          <div style="flex: 1">
            <div class="trend-num">{{ team.stats.offered }}</div>
            <small class="text-light">Offered</small>
          </div>
          <div style="flex: 1">
            <div class="trend-num">{{ team.stats.answered }}</div>
            <small class="text-light">Answered</small>
          </div>
          <div style="flex: 1">
            <div class="trend-num">{{ team.stats.abandoned }}</div>
            <small class="text-light">Abandoned</small>
          </div>
          <div style="flex: 1">
            <div class="trend-num">{{ fmtTalk(team.stats.avg_wait_seconds) }}</div>
            <small class="text-light">Avg wait</small>
          </div>
          <div style="flex: 1">
            <div class="trend-num">{{ team.queue.waiting }}</div>
            <small class="text-light">Waiting now</small>
          </div>
        </div>
      </div>

      <div v-if="manages" class="card vstack">
        <h2>Live calls</h2>
        <p v-if="liveCalls.length === 0" class="text-lighter">No live calls.</p>
        <div v-for="c in liveCalls" :key="c.id" class="row-between">
          <span class="hstack">
            <Badge :tone="callStatusTone(c.status)">{{ c.status }}</Badge>
            <span class="mono">{{ who(c) }}</span>
            <span class="text-light">{{ c.agent_name || "—" }}</span>
          </span>
          <span v-if="c.room_id" class="hstack">
            <Button v-if="active?.callId !== c.id" size="sm" variant="secondary" @click="startSupervise(c)">Listen</Button>
            <template v-else>
              <Button
                v-for="m in (['monitor', 'whisper', 'barge'] as const)"
                :key="m"
                size="sm"
                :variant="active.mode === m ? 'primary' : 'secondary'"
                @click="setMode(m)"
              >
                {{ LABELS[m] }}
              </Button>
              <Button size="sm" variant="danger" @click="stopSupervise()">Stop</Button>
            </template>
          </span>
        </div>
      </div>

      <div class="card vstack">
        <h2>Members</h2>
        <div class="table">
          <table>
            <thead>
              <tr>
                <th>Member</th>
                <th>Status</th>
                <th v-if="seesAll">Manager</th>
                <th v-if="manages"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="m in team.members" :key="m.user_id">
                <td>
                  {{ m.display_name || m.sip_username }}
                  <Badge v-if="m.is_manager" tone="neutral">manager</Badge>
                </td>
                <td><Badge :tone="memberTone(m.status)">{{ m.status }}</Badge></td>
                <td v-if="seesAll">
                  <label>
                    <input type="checkbox" :checked="m.is_manager" @change="setManager(m, ($event.target as HTMLInputElement).checked)" />
                    Manager
                  </label>
                </td>
                <td v-if="manages" class="align-right">
                  <Button v-if="!m.is_manager || seesAll" size="sm" variant="danger" @click="removeMember(m)">Remove</Button>
                </td>
              </tr>
              <tr v-if="team.members.length === 0">
                <td :colspan="seesAll ? 4 : 3" class="align-center text-lighter">No members</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="manages" class="hstack">
          <div style="flex: 1">
            <Select v-model="pick" :options="candidateOptions" placeholder="Add a user…" />
          </div>
          <Button :disabled="!pick" @click="addMember">Add</Button>
        </div>
      </div>

      <div v-if="manages" class="card" style="padding: 0">
        <div class="table">
          <table>
            <thead>
              <tr>
                <th>From</th>
                <th>Agent</th>
                <th>Status</th>
                <th>Started</th>
                <th class="align-right">Dur</th>
                <th>Recording</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="c in team.calls ?? []" :key="c.id">
                <td class="mono">{{ who(c) }}</td>
                <td>{{ c.agent_name || "—" }}</td>
                <td><Badge :tone="callStatusTone(c.status)">{{ c.status }}</Badge></td>
                <td class="text-light" style="white-space: nowrap">{{ fmt(c.started_at) }}</td>
                <td class="align-right mono">{{ c.duration_seconds != null ? fmtTalk(c.duration_seconds) : "—" }}</td>
                <td>
                  <audio v-if="recUrl[c.id]" :src="recUrl[c.id]" controls autoplay style="height: 2rem"></audio>
                  <button v-else-if="c.recording_url" type="button" class="plain" style="color: var(--primary)" @click="playRecording(c)">Play</button>
                  <span v-else class="text-lighter">—</span>
                </td>
              </tr>
              <tr v-if="(team.calls ?? []).length === 0">
                <td colspan="6" class="align-center text-lighter">No calls today</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>
  </div>
</template>
