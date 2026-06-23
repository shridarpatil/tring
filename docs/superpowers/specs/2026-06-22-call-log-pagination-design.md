# Call-log pagination + reusable `<Pagination>` — design

**Date:** 2026-06-22
**Status:** Approved (ready for implementation plan)

## Goal

Add numbered pagination to the call log, and deliver it through a reusable
pagination component that is used by **both** the Calls view and the IVR Flows
list. Pagination controls render at the **bottom** of the table.

## Decisions (locked)

- **Style:** numbered pages with a total count — `312 calls   ‹ 1 2 3 … 13 ›`.
- **Placement:** controls at the bottom of the table, below the rows.
- **Reuse model:** a single presentational `<Pagination>` component. The Calls
  view keeps its bespoke expandable table (it has click-to-expand IVR-path
  detail rows); the Flows list keeps `DataTable`. Both drop in `<Pagination>`.
- **API contract:** a **uniform list envelope** — `{ items, total, limit,
  offset }` — returned by every paginated list endpoint. There are currently no
  external API consumers, so this one-time shape change has no compatibility
  cost; only the internal web console consumes these endpoints.
- **Scope:** wire pagination into **both** `/v1/calls` (Calls view) and
  `/v1/flows` (IVR Flows list) now.
- **Paging mechanism:** offset-based (`limit` + `offset`/`page`). Cursor-based
  paging (`starting_after=<id>`, Stripe-style) is the documented future upgrade
  if exact stable paging is ever needed; the envelope already accommodates it.

## Why a uniform envelope (not opt-in / not headers)

- An **opt-in envelope** (bare array unless `?page=` present) makes the endpoint
  polymorphic — same URL returns an array or an object depending on params.
  Tolerable for an internal UI, but a smell for an API-first product: hard to
  type, hard to document.
- **Headers** (`X-Total-Count` + bare array body) are non-breaking but easy for
  clients to miss and not the convention for this product category.
- A **uniform envelope** is the convention for "Twilio-like" CPaaS APIs (Twilio
  list endpoints wrap in `{calls:[…], page, page_size, …}`; Stripe wraps in
  `{object:"list", data:[…], has_more}`). Bare arrays are the anomaly. Tring is
  pre-1.0 alpha with no external consumers — the cheapest possible moment to set
  the right permanent contract.

## API design

Both `GET /v1/calls` and `GET /v1/flows` always return:

```json
{ "items": [ ... ], "total": 312, "limit": 25, "offset": 50 }
```

- `page` (1-based) and `limit` are **optional** query params.
- With **no `page`**, each endpoint returns its current default volume, just
  wrapped + a `total`:
  - `/v1/calls` → first `callsDefaultLimit` (50) rows, `offset` 0.
  - `/v1/flows` → all flows (small per-tenant set), `offset` 0.
- With `?page=N&limit=L`, returns that page. `limit` defaults to 25 and is
  clamped to the existing `callsMaxLimit` (200). `offset = (page-1) * limit`.
- Existing `?status=` filter on `/v1/calls` is preserved and composes with
  paging; the `total` reflects the filtered set.

### SQL (`queries.sql` + `internal/queries`)

- Add `OFFSET $N` to `list-calls` and `list-calls-by-status`.
- Add a paged `list-flows` variant with `LIMIT/OFFSET` (current `list-flows`
  returns all rows; keep an unpaged path for the no-`page` default).
- Add count queries: `count-calls`, `count-calls-by-status`, `count-flows`
  (same `WHERE` filters as their list counterparts).
- Add matching fields to the `Queries` struct (a missing `-- name:` tag panics
  on boot, so the binding is verified at startup).

### Handlers (`internal/api`)

- `handleListCalls` / `handleListFlows`: parse `page`/`limit`; when paginated,
  run the count query + the offset list query and write the envelope. The
  no-`page` path also returns the envelope (with `total`), preserving current
  default volume.

## Frontend design

### `components/ui/Pagination.vue` (new)

Presentational, controlled component. No data fetching.

- **Props:** `page: number`, `pageSize: number`, `total: number`.
- **Emits:** `update:page`.
- **Render:** a bottom bar — left: `"{total} calls"`-style count; right: numbered
  pager `‹ 1 2 3 … 13 ›`, windowed (first and last page always shown, `…` for
  gaps, current page highlighted, prev/next chevrons disabled at the ends).
- Hidden when `total <= pageSize` (single page).
- Exported from `components/ui/index.ts`.

### `composables/usePagedList.ts` (new)

The paged sibling of `useList`. Owns paging state and the envelope fetch.

- **Signature:** `usePagedList<T>(endpoint, { pageSize?, params? })`.
- **State:** `items`, `page`, `pageSize` (default 25), `total`, `loading`,
  `error`, plus `load()` and a `setPage(n)` helper.
- Fetches `${endpoint}?page=${page}&limit=${pageSize}` plus any `params`, and
  reads `res.items` / `res.total` from the envelope.
- `params` is an optional reactive ref of extra query params (e.g. `{ status }`);
  changing it **resets to page 1** and reloads.
- After a mutation a view calls `load()` to refresh the current page; if the
  current page is now empty and `page > 1`, clamp to the previous page.

### `api/types.ts`

```ts
export type Paginated<T> = { items: T[]; total: number; limit: number; offset: number };
```

### View changes

- **`CallsView.vue`** — replace its bespoke `load()` (currently `?limit=100`)
  with `usePagedList<Call>("/v1/calls", { params })` where `params` carries
  `status`. Keep the bespoke `<table>` with expandable IVR-path rows untouched;
  render `<Pagination>` below it. Status-filter change flows through `params`
  (resets to page 1).
- **`FlowsListView.vue`** — swap `useList` → `usePagedList<Flow>("/v1/flows")`;
  render `<Pagination>` below the existing `DataTable`. Re-fetch the current
  page after create/delete (with last-page clamp).
- **Envelope migration for existing array-consumers** (read `.items`):
  - `DashboardView.vue` — `api.get<Call[]>("/v1/calls?limit=8")` →
    `Paginated<Call>`, use `.items`.
  - `SettingsView.vue` — `api.get<Flow[]>("/v1/flows")` → `.items`.
  - `NumbersView.vue` — `api.get<Flow[]>("/v1/flows")` → `.items`.

## Behaviour details

- Page size: 25.
- Status-filter change resets to page 1.
- Deleting the last item on the last page clamps back one page.
- `<Pagination>` renders nothing when there is only one page.

## Docs

- A short `CHANGELOG.md` note: list endpoints (`/v1/calls`, `/v1/flows`) now
  return a `{ items, total, limit, offset }` envelope and accept `page`/`limit`.

## Out of scope

- Cursor-based pagination (future upgrade path; envelope is forward-compatible).
- Paginating other list endpoints (agents, numbers, webhooks, etc.) — the
  `<Pagination>` component + `usePagedList` make that a drop-in later.
- User-selectable page size.
- Adding expandable rows to the shared `DataTable` (explicitly rejected — Calls
  keeps its own table and shares only `<Pagination>`).

## Testing

- Backend: handler tests for the envelope shape, `total` correctness with and
  without the `status` filter, default (no `page`) volume, and offset math.
  DB-gated (`TRING_TEST_DSN`), consistent with the existing suite.
- Frontend: `<Pagination>` windowing/edge rendering is pure and unit-testable;
  `usePagedList` page-reset-on-param-change and last-page clamp.
