# Keel: Product Blueprint

Status: agreed
Last updated: 2026-10-16

## 1. What we are building

Keel is a web panel for running servers and the websites on them. It brings together three products people already know:

| Inspiration | What we take from it |
|---|---|
| **Ploi** | Smooth app deployment: connect a Git repo, push code, the site updates itself. |
| **Cloudways** | Managed hosting: backups, staging copies, monitoring and alerts handled for you. |
| **aaPanel** | Full server control: files, databases, cron, firewall, services, all from the browser. |

The promise to the user: *point the panel at a fresh server, and from then on you never need to SSH in unless you want to.*

## 2. Who it is for

- **Developers and small agencies** who run their own VPS servers (DigitalOcean, Hetzner, Vultr, AWS, etc.) and host several sites or apps.
- One account can manage many servers. Teams with roles come after the first version.

## 3. How the pieces fit together

```
   Browser (React + Tailwind)
            │  HTTPS / JSON + WebSocket
            ▼
   ┌───────────────────┐        ┌──────────────┐
   │   Panel (Go API)  │◀──────▶│  PostgreSQL  │
   │  users, servers,  │        └──────────────┘
   │  sites, jobs      │
   └───────────────────┘
            ▲  secure WebSocket (agent dials out)
   ┌────────┴────────┬─────────────────┐
   ▼                 ▼                 ▼
 Agent (Go)       Agent (Go)        Agent (Go)
 Server A         Server B          Server C
```

**Panel.** One Go service that serves the API, keeps the database, and sends work to servers. It runs on its own machine, separate from the servers it manages.

**Agent.** A small Go program installed on every managed server. It reports health stats and carries out jobs the panel sends (install software, create a site, run a deploy, and so on). The agent **connects out** to the panel, so managed servers do not need to open any extra inbound port.

**Web app.** A React single-page app styled with Tailwind CSS. Every component is built in-house; no component libraries (no shadcn, MUI, etc.).

### Key decisions

| Topic | Decision | Reason |
|---|---|---|
| Panel ↔ server link | Agent opens a TLS WebSocket to the panel and keeps it open | Works behind firewalls/NAT, real-time stats, no SSH keys stored in the panel |
| Linking a server | One-line install command with a one-time token | Same experience as Ploi; token is swapped for a long-lived agent credential |
| Jobs | Panel sends named, typed jobs; agent streams logs back | Makes every action auditable and its output visible live in the browser |
| Agent safety | Agent only runs actions from a fixed list, never arbitrary shell from the panel (except explicit deploy scripts the user writes) | Limits damage if the panel is ever compromised |
| Database | PostgreSQL | Reliable, good for multi-user, easy to host |
| Supported servers (v1) | Ubuntu 22.04 and 24.04 LTS | Covers most VPS users; other distros later |
| Web stack on servers | Nginx, PHP-FPM (multiple versions), MySQL/MariaDB, Redis, Node.js, Let's Encrypt | What Ploi and Cloudways users expect |

## 4. Features and release order

Each milestone matches a step in the agreed plan. "v1" means the first usable release (end of milestone 3).

**Status (November 2026):** milestones 1 to 3 are done, so v1 is complete. Milestone 4 has not started.

### Milestone 1: Blueprint (this document) (done)
Agree on scope and architecture.

### Milestone 2: Link a server (done)
- Sign up, log in, log out (email + password, sessions)
- Add server: panel shows a one-line install command
- Agent installs itself as a system service and connects back
- Server list with online/offline status
- Server dashboard: CPU, memory, disk, load, uptime, live graphs
- List of system services (nginx, mysql, etc.) with start / stop / restart

### Milestone 3: Websites and deployments (the Ploi part) (done)
- Install the web stack from the panel (pick PHP versions, database engine)
- Create a site: domain, web root, PHP version, Nginx config generated for you
- Free SSL with Let's Encrypt, auto-renewal
- Connect GitHub; deploy a branch on push via webhook
- Editable deploy script with live log output and deploy history
- Environment file (`.env`) editor
- Create databases and database users

**This is v1.**

### Milestone 4: Hosting and power tools (the Cloudways and aaPanel parts)
Hosting:
- Scheduled backups of files and databases (local + S3-compatible storage), one-click restore
- Staging copy of a site, push staging to live
- Monitoring alerts (server offline, disk nearly full, high load, SSL expiring) by email

Server tools:
- File manager (browse, upload, edit, permissions)
- Cron job manager
- Firewall rules (UFW)
- Database browser
- Server user and SSH key management

### Later (not scheduled)
- Teams, roles and audit log
- Provision servers directly via cloud provider APIs (DigitalOcean, Hetzner, AWS)
- GitLab / Bitbucket
- Queue workers and daemons (Supervisor)
- One-click apps (WordPress, Laravel, Node apps)
- Two-factor login
- Debian / other distro support

## 5. Security basics

- All traffic over TLS in production. Agents can pin the panel's certificate (by public key by default), set with `KEEL_TLS_FINGERPRINT` and passed to them at install.
- Install tokens are single-use and expire after 1 hour.
- Secrets the panel must keep (database passwords, webhook secrets, the GitHub App's private key) are encrypted at rest with `KEEL_SECRET_KEY`, which `keel-panel init` creates. The panel won't start without it.
- Some secrets never reach the panel: a site's `.env` is read and saved on the server only, and deploy keys' private halves stay on the server. Job history never holds secrets; they are added only when a job is sent.
- The agent runs only fixed, typed actions with values checked on both sides. Each site has its own system user, PHP-FPM pool and home folder; deploy scripts, git and `.env` access run as that user, never as root.
- Config changes are tested before they take effect (`nginx -t`, `php-fpm --test`) and rolled back if rejected.
- Webhooks are trusted only with a valid GitHub signature.
- Rate limits and lockout on login.

## 6. Repository layout

```
control-portal/
├── panel/     Go: API server, database migrations, job runner, GitHub App client
├── agent/     Go: server agent (the installer script is served by the panel)
├── protocol/  Go: messages and validation shared by panel and agent
├── web/       React (plain JavaScript) + Tailwind (Vite) front end
├── scripts/   Local test environment (LXC containers, Pebble)
├── docs/      This spec and later design notes
└── README.md
```

Panel and agent share message types and input checks through the `protocol` package, so the protocol stays in sync and both sides refuse the same bad values.

## 7. Decisions

1. **Web app language**: plain JavaScript, no TypeScript.
2. **Hosting the panel**: designed for self-hosting first; keep multi-tenant (offering it as a hosted service) possible later.
3. **Product name**: **Keel**. A keel is the backbone that keeps a ship steady. Binaries and services use the `keel-` prefix (`keel-panel`, `keel-agent`). The repository keeps its current name for now.

Milestone 3 (October and November 2026):

4. **GitHub**: each site deploys through its own deploy key and webhook, or through an optional GitHub App per account, which issues a short-lived read-only token per deploy. Public repositories also work over HTTPS without a key. GitLab and Bitbucket come later.
5. **`.env`**: lives on the server only. The editor reads and writes it through the agent, as the site user; the panel stores no copy, so editing needs the server online.
6. **Database passwords**: generated by the panel, stored encrypted, and can be shown again.
7. **Secret key**: `keel-panel init` writes `KEEL_SECRET_KEY` to the panel's env file; the panel refuses to start without a valid one.
8. **Certificate pinning**: the panel runs behind a proxy and can't see its own certificate, so the fingerprint is set explicitly (`keel-panel fingerprint` prints it). Pins cover the public key by default, which survives renewals that keep the key (`certbot renew --reuse-key`).
9. **Deploys need no sudo**: PHP-FPM isn't reloaded after a deploy; OPcache picks up changed files. The deploy script runs as the site user with `php` set to the site's version.
10. **Versions offered**: PHP 7.4 to 8.5 (ondrej/php) and Node.js 22, 24 and 26 (NodeSource), the release lines still supported upstream; the plan's Node 18 and 20 had reached end of life.
11. **Testing**: unprivileged LXC containers stand in for VPSes and run PostgreSQL, and Pebble stands in for Let's Encrypt (`scripts/lxc-dev.sh`). Any PostgreSQL, including Docker, works for the panel.
