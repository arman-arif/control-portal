# Keel

Keel is a self-hosted control panel for your servers: Ploi-style deployments, Cloudways-style managed hosting and aaPanel-style server tools in one place. See [docs/SPEC.md](docs/SPEC.md) for the full blueprint.

## What works today (milestone 2, milestone 3 in progress)

- Create the owner account and log in
- Add a server and get a one-line install command
- The Keel agent installs itself as a systemd service and connects back to the panel
- Server list with online/offline status
- Live CPU, memory, disk, load and uptime for each server
- Start, stop and restart common services (nginx, MySQL, PHP-FPM, Redis and others)
- Jobs: longer server tasks are queued, run one at a time per server, and show their output live with a history (first job: refresh package lists)

## Layout

```
panel/     Go: API server, database migrations, agent hub, job runner
agent/     Go: the agent that runs on each managed server
protocol/  Go: messages shared by the panel and the agent
web/       React (plain JavaScript) + Tailwind front end
docs/      Blueprint and design notes
```

## Running the panel locally

You need Go 1.24+, Node 20+ and PostgreSQL.

```sh
createdb keel
make            # builds web/dist, dist/keel-agent-linux-*, dist/keel-panel

KEEL_DATABASE_URL=postgres://localhost/keel \
KEEL_PUBLIC_URL=http://localhost:8080 \
./dist/keel-panel
```

Open http://localhost:8080 and create the owner account. The first account becomes the owner; after that sign-up is closed unless you set `KEEL_ALLOW_SIGNUP=true`.

For front-end work, run the panel as above and `npm run dev` in `web/`. Vite proxies API calls to the panel.

### Panel settings

| Variable | Default | Meaning |
|---|---|---|
| `KEEL_DATABASE_URL` | (required) | PostgreSQL connection string |
| `KEEL_PUBLIC_URL` | `http://localhost:8080` | Address servers and browsers use to reach the panel. Use `https://` in production. |
| `KEEL_LISTEN` | `:8080` | Address the panel listens on |
| `KEEL_WEB_DIR` | `web/dist` | Built web app |
| `KEEL_AGENT_DIR` | `dist` | Folder with `keel-agent-linux-amd64` and `keel-agent-linux-arm64` |
| `KEEL_ALLOW_SIGNUP` | `false` | Allow more accounts after the owner |
| `KEEL_TRUST_PROXY` | `false` | Read the client IP from `X-Forwarded-For` when behind a reverse proxy |

## Linking a server

In the panel, click **Add server**, then run the command it shows on a fresh Ubuntu 22.04/24.04 server as root:

```sh
curl -fsSL https://panel.example.com/install.sh | sudo bash -s -- <token>
```

The token works once and expires after an hour. The agent connects out to the panel over a WebSocket, so the server does not need any extra inbound port.

## Tests

```sh
make test
# also run the API tests against a real database:
KEEL_TEST_DATABASE_URL=postgres://localhost/keel_test make test
```
