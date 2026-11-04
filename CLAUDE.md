# Keel (repo: control-portal)

Self-hosted control panel for servers and their sites. A **panel** (Go API + PostgreSQL + built web app) runs on its own machine; each managed server runs a **keel-agent** that dials *out* to the panel over a WebSocket. The agent only runs actions from the fixed allow-list in `protocol/protocol.go`, never arbitrary shell. Product blueprint and milestones: `docs/SPEC.md`. User-facing setup and settings: `README.md`.

## Stack (do not change without asking Ariful)

- Go, standard library `net/http` with 1.22+ pattern routing. No web framework.
- PostgreSQL via `github.com/jackc/pgx/v5`. Migrations are `panel/store/migrations/NNN_*.sql`, embedded and applied on panel start. Add a new numbered file; never edit an applied one.
- Panel ↔ agent: `github.com/coder/websocket`; message types live in `protocol/`.
- Dependencies are pinned to versions that build on **Go 1.24**. Always run Go with `GOTOOLCHAIN=local`.
- Frontend: React 18 in **plain JavaScript (no TypeScript)**, Vite 5, React Router 6.
- Tailwind CSS v4 via `@tailwindcss/vite`. `tailwindcss` and `@tailwindcss/vite` must stay pinned to the **exact same version**.
- **No UI component libraries.** Shared UI bits go in `web/src/components/ui.jsx`.
- Self-hosted first, but keep multi-tenant possible: owned rows carry `user_id`, and queries scope by it.
- Names: product is Keel; binaries, services and env vars use the `keel-`/`KEEL_` prefix.
- Target servers: Ubuntu 22.04 and 24.04 LTS with systemd.

## Layout

- `panel/main.go` commands (`serve | init | fingerprint`), config, DB, HTTP server. `panel/init.go` and `panel/fingerprint.go` the other commands. `panel/api/` routes and handlers (`api.go` has the routes, CSRF guard and SPA server). `panel/hub/` live agent connections, in-memory stats, job dispatch. `panel/store/` queries and migrations. `panel/secrets/` AES-256-GCM for values stored in the database (`BYTEA` columns holding nonce‖ciphertext).
- `agent/` the `keel-agent` binary (`register | run | repin | version`). Linux only: it reads `/proc`. `agent/pin.go` holds the HTTP client every panel connection must use, which enforces the TLS pin.
- `protocol/` envelope, message types, allow-listed services and actions, TLS fingerprints.
- `web/src/` `api.js` (fetch wrapper), `pages/`, `components/`.

## API conventions

- Non-GET `/api/` requests from the browser must send `X-Keel-Request: 1` (`web/src/api.js` does this). Agent endpoints under `/api/agent/` are exempt.
- Sessions are the `keel_session` cookie.

## Running locally

```sh
export GOTOOLCHAIN=local
make                                   # web/dist, dist/keel-agent-linux-{amd64,arm64}, dist/keel-panel
go run ./panel init --env-file .keel-dev.env   # once; the panel refuses to start without KEEL_SECRET_KEY
set -a; . ./.keel-dev.env; set +a
KEEL_DATABASE_URL=postgres://keel:keel@localhost:5432/keel \
KEEL_PUBLIC_URL=http://localhost:8080 go run ./panel     # run from repo root
cd web && npm run dev                  # :5173, proxies /api, /install.sh, /downloads to :8080
```

Agent dev loop on Linux, without root (token from **Add server** in the UI):

```sh
go run ./agent register --panel http://localhost:8080 --token <TOKEN> --config ./.agent-dev.json
go run ./agent run --config ./.agent-dev.json
```

Never commit `.agent-dev.json`; it holds the agent secret. `.keel-dev.env` holds the panel's secret key and is gitignored.

## Checks (run before calling work done)

```sh
GOTOOLCHAIN=local KEEL_TEST_DATABASE_URL=postgres://keel:keel@localhost:5432/keel_test make test
gofmt -l .                             # must print nothing
cd web && npm run build                # the only frontend check; there is no JS test runner or linter
```

API tests skip without `KEEL_TEST_DATABASE_URL`. Each test creates and drops its own `keel_test_<timestamp>` schema.

## Git workflow

- One feature branch and PR per plan step, each leaving `main` working. Update README "What works today" when a step adds something users can do.
- PRs are squash-merged. Don't force-push; to update a branch, merge `main` into it.
- Ask Ariful before starting a new milestone. A merged PR is not a go-ahead for the next one.
