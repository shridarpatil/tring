# Dashboard redesign — Live Ops cockpit

**Date:** 2026-06-23
**Status:** Approved design, ready for implementation plan
**Scope:** Replace the current admin-console dashboard (6 flat stat cards + recent-calls table) with a real-time "Live Ops cockpit" — a screen an operator keeps open all day showing capacity (agents) vs demand (queue) vs in-flight (live calls), plus a running "today so far" trend strip. All panels render real data.

## Goal

Turn `web/src/views/DashboardView.vue` from a static snapshot into a live operations monitor. The mental model is an air-traffic-control screen: at a glance you see how many calls are live, which teams are backing up, who's available to take the next call, and how today's volume is trending.

Non-goals (explicitly out of scope for this work):
- Historical analytics beyond "today" (no date-range reporting, no per-agent historical stats).
- Click-through actions from the cockpit (barge/whisper, force-hangup). Read-only monitor.
- Longest-wait metric (rejected — would require new timestamp publishing in the shared `internal/routing` brain; revisit later).

## Layout

Top to bottom, full-width container (`space-y-4`/`space-y-6` rhythm matching the rest of the console):

1. **Trend strip** (full-width card) — "today so far":
   - Left: three running stats — **Calls today**, **Answered %** (success-toned), **Avg talk** (mm:ss).
   - Divider, then a labelled **calls-per-hour mini bar chart** for the last 12 hours. Each bar carries its count above and its hour below (`9AM, 10AM … 8PM`, consistent 12-hour format). Current hour highlighted in live-amber.

2. **KPI hero row** (4 tiles, responsive grid):
   - **On call now** — count + the existing animated `LevelMeter` (active only when `> 0`); amber when live.
   - **Waiting in queue** — total across teams; amber + signal dot when `> 0`.
   - **Agents available** — `available / total` (e.g. `9 / 14`).
   - **Answered today** — answered-today % (from the stats endpoint). *(Chosen over "Longest wait" to avoid cross-plane routing changes.)*

3. **Main row** (two columns, ~62% / ~38%):
   - **Left — Live calls** panel: one row per in-progress call: source icon (phone/web/whatsapp), `caller → team/destination`, masked number or flow name, a `talking`/`ringing` status tag, and a live-ticking duration. Panel header shows a quiet `N active` count. Data updates in place on each poll with **no refresh indicator and no flash/blink** — the live-ticking durations make it read as realtime on their own.
   - **Right (stacked)**:
     - **Queues by team** panel: per-team waiting count + a pressure bar (amber past a threshold, teal otherwise, faint at 0).
     - **Agents** panel: per-agent row with status dot (online/on-call/away/offline) and a short status label (`on call · mm:ss`, `available`, `away`, `offline`).

Reuse existing UI primitives (`Card`, `Panel`, `Badge`, `SignalDot`, `LevelMeter`, `DataTable` where a table fits). New presentational pieces are small and local (see Frontend components).

## Data sources

| Panel / value | Source | Status |
|---|---|---|
| On call now | `GET /v1/queues` → `on_call` | exists (`handlers_queues.go`) |
| Waiting in queue (total + per team) | `GET /v1/queues` → `total`, `queues[]` | exists |
| Agents available / total + per-agent status | `GET /v1/agents` (has live `status`) | exists (`handlers.go`) |
| Live calls list | `GET /v1/calls?active=true` | **new filter** on existing endpoint |
| Trend strip (calls today, answered %, avg talk, hourly[]) | `GET /v1/stats/overview` | **new endpoint** |

### New backend work

**A. Active-calls filter on `GET /v1/calls`.**
Add an `active=true` query param that returns only in-progress calls (`ended_at IS NULL AND status IN ('ringing','answered')`), ordered by `started_at ASC` (oldest first, so the longest-running call is stable at the top). Implementation mirrors the existing `status` filter branch in `handlers_calls.go`:
- New `queries.sql` block `-- name: list-active-calls` (same column list as `list-calls`, different WHERE/ORDER, no pagination — active calls are inherently bounded; cap with `LIMIT 100` as a safety valve).
- New `Queries` field `ListActiveCalls string \`query:"list-active-calls"\``.
- Handler branch: when `active=true`, use the new query; otherwise existing behavior unchanged. Returns the same `Call` shape the frontend already knows.

**B. New stats endpoint `GET /v1/stats/overview`.**
Tenant-scoped, registered under the `AuthRequired` group in `server.go`. Returns:
```json
{
  "calls_today": 142,
  "answered_pct": 91,
  "avg_talk_seconds": 204,
  "hourly": [ { "hour": "2026-06-23T09:00:00Z", "count": 4 }, ... ]
}
```
Backed by two new `queries.sql` blocks (+ matching `Queries` fields):
- `-- name: stats-today` — single row: `count(*)`, `count(*) FILTER (WHERE answered_at IS NOT NULL)`, `avg(duration_seconds) FILTER (WHERE duration_seconds IS NOT NULL)`, all `WHERE tenant_id = $1 AND started_at >= date_trunc('day', now())`. Compute `answered_pct` in Go (guard divide-by-zero → 0).
- `-- name: stats-hourly` — `SELECT date_trunc('hour', started_at) AS hour, count(*) FROM calls WHERE tenant_id = $1 AND started_at >= now() - interval '12 hours' GROUP BY 1 ORDER BY 1`. Handler fills gaps so all 12 hours are present (zero-count hours included) — keeps the chart x-axis stable.

Timezone: bucket in UTC at the DB; the frontend renders hour labels in the browser's locale (matches how `started_at` is already displayed via `toLocaleString()`). Note this in the handler so it isn't "fixed" later by accident.

New handler file: `internal/api/handlers_stats.go` (`handleStatsOverview`), following the `handlers_queues.go` shape (tenant from ctx, query, `writeJSON`).

## Frontend

Rewrite `web/src/views/DashboardView.vue`. New TypeScript types in `web/src/api/types.ts` mirroring the two new responses (`StatsOverview`, `HourlyBucket`; reuse `Call` for active calls).

Polling: keep the existing `usePoll` composable. Poll `/v1/queues`, `/v1/calls?active=true`, `/v1/agents`, and `/v1/stats/overview` on the same **5s** cadence. The trend strip can poll on a slower cadence (e.g. 30s) since hourly buckets move slowly — but a single 5s cycle for everything is acceptable for v1 and simpler; pick one and note it. Live durations tick client-side every 1s between polls (compute from `started_at`/`answered_at`, don't wait for the server).

New small presentational components under `web/src/components/` (kept local and focused — single purpose each):
- `MiniBarChart.vue` — the labelled calls/hour bars (props: `buckets`, `highlightLast`).
- `QueuePressureBar.vue` — team row + pressure bar (or inline in the view if trivial).
- Live-call row and agent row can be inline in the view or extracted if they grow.

There is **no** refresh-countdown component — polling is silent.

Reuse `LevelMeter` for the on-call meter (already animated via `animate-level`).

## Motion

Polling is **silent** — no countdown ring, no refresh flash/blink on the Live-calls panel. The realtime feel comes entirely from data updating in place plus:
- **On-call level meter** — existing `LevelMeter`, active when `on_call > 0`. Consistent with the live-signal language and gated behind `prefers-reduced-motion` (as the app already does for `animate-pulse-ring`/`animate-level`).
- **Live-ticking durations** — call durations and agent on-call timers tick every second (see Frontend → Polling); since they're derived from `started_at` + the current clock they're always accurate and update smoothly without any poll.

## Error / loading / empty states

- Initial load: existing "Loading…" until the first batch resolves.
- Per-panel resilience: `/v1/queues` is already best-effort (keeps last value on error) — extend the same tolerance to the new calls/stats polls so one failing endpoint doesn't blank the whole cockpit. Show a subtle "stale" hint on a panel whose last poll failed rather than throwing the page into the error state.
- Empty states: "No live calls", "No callers waiting", "No agents online" — quiet, not alarming.
- Hard error only if the very first load fails entirely (existing `error` banner).

## Testing

- **Backend (Go):** unit-test the stats aggregation handler logic where it's pure (answered-% divide-by-zero guard, hourly gap-filling producing exactly 12 buckets). DB-dependent query tests follow the `internal/dbtest` pattern (skip unless `TRING_TEST_DSN` set). Active-calls filter: a query test asserting only `ended_at IS NULL` rows return.
- **Frontend:** keep it light — the existing console has no heavy test harness. Verify by running `cd web && npm run build` (type-check passes) and manual check against the dev API. `MiniBarChart` and `RefreshRing` are pure enough to eyeball.
- **Manual verification:** load the dashboard against a running stack with a live call in progress; confirm live durations tick every second, new/ended calls appear/disappear on the 5s poll without any flash, the meter animates only when on-call > 0, and a backing-up team turns its pressure bar amber.

## Files touched (summary)

New:
- `internal/api/handlers_stats.go`
- `web/src/components/MiniBarChart.vue`, `QueuePressureBar.vue`

Edited:
- `queries.sql` (+3 blocks: `list-active-calls`, `stats-today`, `stats-hourly`) and `internal/queries/queries.go` (+3 fields)
- `internal/api/handlers_calls.go` (active filter branch), `internal/api/server.go` (register `/v1/stats/overview`)
- `web/src/views/DashboardView.vue` (rewrite), `web/src/api/types.ts` (+stats types)

## Open questions / decisions made

- 4th KPI = **Answered today** (not Longest wait) — locked, avoids cross-plane routing changes.
- Poll cadence: single 5s cycle for all panels in v1 (trend strip could be slower later).
- Hourly window fixed at last 12h for v1; configurable window is future work.
