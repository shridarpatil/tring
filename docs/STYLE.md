# Tring — Code Style & Conventions

Tring follows the **Zerodha / Kailash Nadh Go style** — pragmatic, flat,
minimal-dependency, no-magic. References: [knadh/koanf](https://github.com/knadh/koanf)
(library) and [listmonk](https://github.com/knadh/listmonk) (the canonical
app structure). This doc is the short version; when in doubt, match those.

## Principles
1. **Flat over nested.** Packages are named by what they do and live directly
   under `internal/`. Avoid deep hierarchies; clarity beats taxonomy.
2. **Minimal dependencies.** Stdlib first. A new dependency must earn its
   place — prefer a small focused lib (chi, pgx, koanf) over a framework that
   pulls a tree. Justify additions in the PR.
3. **No global state.** Dependencies live on a struct (the `Server`,
   `Executor`, `Notifier`, …) created in `main` and passed down. No package
   globals for db/config/loggers.
4. **Composition, small interfaces.** Prefer narrow interfaces (e.g.
   `provider.Provider`) and plain structs over inheritance-like patterns.
5. **Boring and explicit.** Errors returned and handled, not panicked.
   Comments explain *why*, not *what*.

## Folder structure
```
cmd/<binary>/main.go     # one dir per binary (api, orchestrator); wiring only
internal/<pkg>/          # flat, function-named packages:
  api/                   #   HTTP surface (REST, console auth, /fs, /sdk, /admin)
  orchestrator/          #   FreeSWITCH-facing ESL service + IVR executor
  config/                #   koanf config loader (defaults + TRING_* env)
  queries/               #   loads the SQL catalog into a typed Queries struct
  db/ migrate/ bootstrap/#   data layer, schema migrations, first-boot seed
...
queries.sql              # the single named-SQL catalog (repo root, listmonk-style)
embed.go                 # root package: go:embed of queries.sql (can't reach a parent dir)
  provider/ dial/        #   trunk providers, number normalization
  masking/ webhook/      #   domain logic
  freeswitch/            #   ESL client, gateway/registration helpers
  webui/ widget/         #   //go:embed of the built console + browser bundles
web/                     # Vue 3 admin console (Vite + Tailwind), embedded
sdk/js/                  # browser SDK (esbuild → voice.js / agent.js / tring.js)
db/  docs/  freeswitch/  # schema source, docs, FS config
```
- A binary's `main.go` is **wiring only**: load config, open deps, construct
  the service struct, run it. Logic lives in `internal/`.
- New shared logic → a new flat `internal/<pkg>`; don't bury it.

## Configuration — koanf
Config is **koanf** (`internal/config`): built-in defaults overlaid with
`TRING_*` environment variables. Keys are flat and lowercase — the env var
minus the `TRING_` prefix:

| Env var | koanf key |
|---|---|
| `TRING_DSN` | `dsn` |
| `TRING_ESL_ADDR` | `esl_addr` |
| `TRING_FS_BASIC_AUTH` | `fs_basic_auth` |

- Add a new setting in **one place**: `internal/config/config.go` (`defaults`
  map). Read it in `main` via `k.String("...")`.
- **Precedence is flag > env > default**: a CLI flag defaults to the
  koanf-resolved value (`flag.String("dsn", k.String("dsn"), ...)`), so
  passing the flag overrides env, which overrides the built-in default.
- Don't reach for `os.Getenv` in new code — go through `config`.

## Database / SQL
- Every non-`tenants` query filters by `tenant_id` (app-level tenancy).
- **All SQL lives in `queries.sql` at the repo root** — one catalog for the
  whole app (the listmonk layout). It's embedded by the root `embed.go`
  (`go:embed` can't reach a parent dir, so the directive sits beside the file);
  `internal/queries` parses those bytes at startup into the `Queries` struct,
  binding each `query:"<tag>"` field to its `-- name: <tag>` block. A struct field with
  no matching block panics at construction (`queries.MustLoad`), so a typo
  surfaces on first boot/test, not at the call site.
  - **Add a query:** add a `-- name: foo` block to `queries.sql` and a
    `Foo string `query:"foo"`` field to `Queries`. Reference it as `s.q.Foo`
    (api), `r.q.Foo` / `e.Q.Foo` (orchestrator), `n.q.Foo` (webhook).
  - **No inline SQL string literals** in production handlers/services — the
    only exceptions are `internal/dbtest` and `*_test.go` fixtures.
  - pgx caches prepared statements per connection, so referencing the strings
    by name has no runtime cost; the win is auditability.
  - Prefer two static named queries over building SQL with `+` at runtime
    (e.g. `list-calls` / `list-calls-by-status` instead of an `if` that
    appends a `WHERE` clause).
- Schema changes are migrations under `internal/migrate/migrations/` — never
  edit an applied migration; add a new one.

## Naming
- **Packages:** short, lowercase, single word, named for function
  (`masking`, `provider`, `webhook`). No `util`/`common`/`helpers` grab-bags.
  Avoid stutter (`provider.Provider`, not `provider.ProviderType`).
- **Files:** one concern per file, named for it (`handlers_calls.go`,
  `gateway_status.go`, `jwt.go`). Tests are `<file>_test.go`.
- **Receivers:** short (`s *Server`, `e *Executor`, `c *Client`).
- **Exported vs unexported:** export only what other packages need. Helpers
  stay lowercase.

## HTTP handlers
- Methods on the service struct (`func (s *Server) handleX`), registered in
  `server.go`'s router. Auth is chi middleware (`AuthRequired`,
  `JWTAuthRequired`). Keep handlers thin; push logic into helpers.
- New browser-hit endpoint → `/v1/sdk/...` (agent JWT) or the public guest
  path; CRM-backend endpoint → `/v1/...`; FreeSWITCH callback → `/fs/...`.

## Embedded assets
- The console (`web/`) and browser bundles (`sdk/js`) are embedded via
  `//go:embed` (`internal/webui`, `internal/widget`) using the
  `.gitkeep`-placeholder pattern so a backend-only `go build` still compiles.

## Comments & docs
- Package doc comment at the top of the primary file (what the package is for).
- Doc comment above every exported symbol.
- Inline comments explain non-obvious *why* (gotchas, trade-offs), as the
  existing code does liberally — keep that.
