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
- Agents can pin the panel's TLS certificate, so they only ever connect to your panel
- Web stack: see what is installed and install Nginx, PHP 7.4 to 8.5 (several side by side), MySQL or MariaDB, Redis, Node.js 22/24/26 and Certbot from the server's Stack tab, with live output
- Sites: create a PHP site with its own system user, PHP-FPM pool and Nginx config; change its PHP version, aliases and web directory; delete it, optionally with its files
- HTTPS: get a free Let's Encrypt certificate for a site in one click; HTTP redirects to HTTPS, new aliases are added to the certificate, and Certbot renews it automatically
- Databases: create MySQL/MariaDB databases and users, choose which databases each user may use, and show or change a user's password (stored encrypted in the panel)
- Deploys: deploy a site from GitHub (a private repository through the site's own deploy key, or a public one) by hand or on every push through a signed webhook; an editable deploy script runs as the site user, with live logs and a history

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

./dist/keel-panel init --env-file .keel-dev.env   # once: creates the secret key
set -a; . ./.keel-dev.env; set +a

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
| `KEEL_SECRET_KEY` | (required) | Key that encrypts secrets stored in the database: 32 bytes, base64. Create it with `keel-panel init`. |
| `KEEL_PUBLIC_URL` | `http://localhost:8080` | Address servers and browsers use to reach the panel. Use `https://` in production. |
| `KEEL_LISTEN` | `:8080` | Address the panel listens on |
| `KEEL_WEB_DIR` | `web/dist` | Built web app |
| `KEEL_AGENT_DIR` | `dist` | Folder with `keel-agent-linux-amd64` and `keel-agent-linux-arm64` |
| `KEEL_ALLOW_SIGNUP` | `false` | Allow more accounts after the owner |
| `KEEL_TRUST_PROXY` | `false` | Read the client IP from `X-Forwarded-For` when behind a reverse proxy |
| `KEEL_TLS_FINGERPRINT` | (none) | Fingerprint of the panel's TLS certificate for new agents to pin. See [Pinning the panel's certificate](#pinning-the-panels-certificate). |
| `KEEL_ACME_SERVER` | (Let's Encrypt) | ACME directory URL for site certificates. Set it only for testing, e.g. Let's Encrypt staging or a local Pebble. |

### Secret key

The panel won't start without `KEEL_SECRET_KEY`. On a production panel, run this once as root:

```sh
keel-panel init     # writes the key to /etc/keel/panel.env, readable only by root
```

Then load that file with `EnvironmentFile=/etc/keel/panel.env` in the panel's systemd unit. Use `--env-file PATH` to write somewhere else. Running `init` again never replaces a key the file already has.

**Back the key up separately from your database backups.** Secrets encrypted with it can't be recovered without it.

## Linking a server

In the panel, click **Add server**, then run the command it shows on a fresh Ubuntu 22.04/24.04 server as root:

```sh
curl -fsSL https://panel.example.com/install.sh | sudo bash -s -- <token>
```

The token works once and expires after an hour. The agent connects out to the panel over a WebSocket, so the server does not need any extra inbound port.

## Pinning the panel's certificate

With a pin, an agent connects only to a panel presenting that certificate, on top of the usual check against trusted CAs. This protects agents if a CA ever issues a certificate for your panel's domain to someone else. It only applies when `KEEL_PUBLIC_URL` is `https://`. The panel doesn't terminate TLS itself, so you tell it the fingerprint of the certificate your reverse proxy serves:

```sh
keel-panel fingerprint https://panel.example.com
# KEEL_TLS_FINGERPRINT=spki-sha256:...
```

Add that line to the panel's env file and restart the panel. Servers linked after that pin it automatically. To pin a server that is already linked, run `keel-agent repin` on it, as shown below. Without the setting, agents still check the certificate against trusted CAs, and the panel logs a warning at startup.

**Keep the same private key when your certificate renews.** The default fingerprint covers the certificate's public key, so it survives a renewal only if the key stays the same. Certbot creates a new key at each renewal unless you tell it not to, so renew with `certbot renew --reuse-key`. If you use `keel-panel fingerprint --cert`, the pin covers the whole certificate and changes at every renewal.

If the key does change, agents refuse to connect and show as offline until you re-pin them. Update `KEEL_TLS_FINGERPRINT` on the panel, then run this on each server:

```sh
sudo keel-agent repin --fingerprint spki-sha256:...
sudo systemctl restart keel-agent
```

`repin` won't save a fingerprint that the panel isn't presenting. Get the new value from your own copy of the certificate if you suspect the connection is being intercepted, since that's exactly what the pin guards against.

## Local test servers (LXC)

`scripts/lxc-dev.sh` runs PostgreSQL and two stand-in VPSes (Ubuntu 24.04 and 22.04, with systemd) as unprivileged LXC containers, so you can link real servers without a cloud account. The script header lists the one-time LXC setup.

```sh
scripts/lxc-dev.sh up                    # create/start keel-db, keel-vps-noble, keel-vps-jammy
eval "$(scripts/lxc-dev.sh env)"         # KEEL_DATABASE_URL, KEEL_TEST_DATABASE_URL, KEEL_PUBLIC_URL
./dist/keel-panel init --env-file .keel-dev.env   # once: creates the secret key
set -a; . ./.keel-dev.env; set +a
./dist/keel-panel
scripts/lxc-dev.sh link keel-vps-noble '<install command from Add server>'
scripts/lxc-dev.sh reset                 # destroy the VPS containers (keeps keel-db)

# HTTPS without real DNS: Pebble stands in for Let's Encrypt
scripts/lxc-dev.sh pebble                # create keel-pebble; VPSes trust its endpoint
eval "$(scripts/lxc-dev.sh env)"         # now also sets KEEL_ACME_SERVER; restart the panel
scripts/lxc-dev.sh acme-host keel-vps-noble shop.keel.test www.shop.keel.test
```

Pebble validates challenges for real against the VPS, but its certificates come from a throwaway root, so use `curl -k --resolve shop.keel.test:443:<VPS IP>` to look at a site. `certbot renew --dry-run` on a VPS always asks Let's Encrypt's staging server, which can't reach `.test` names, so it fails there; real renewals use Pebble. Inside containers, CPU, load and disk show the host's figures. Any local or Docker PostgreSQL works just as well for the panel.

## Tests

```sh
make test
# also run the API tests against a real database:
KEEL_TEST_DATABASE_URL=postgres://localhost/keel_test make test
```
