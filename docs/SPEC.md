# Control Portal: Product Blueprint

Status: draft for review
Last updated: 2026-10-16

## 1. What we are building

Control Portal is a web panel for running servers and the websites on them. It brings together three products people already know:

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

### Milestone 1: Blueprint (this document)
Agree on scope and architecture.

### Milestone 2: Link a server
- Sign up, log in, log out (email + password, sessions)
- Add server: panel shows a one-line install command
- Agent installs itself as a system service and connects back
- Server list with online/offline status
- Server dashboard: CPU, memory, disk, load, uptime, live graphs
- List of system services (nginx, mysql, etc.) with start / stop / restart

### Milestone 3: Websites and deployments (the Ploi part)
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

- All traffic over TLS. Agent pins the panel's certificate fingerprint received at install.
- Install tokens are single-use and expire after 1 hour.
- Secrets stored in the panel (deploy keys, `.env` contents, backup credentials) are encrypted at rest.
- Agent runs as root only for the actions that need it; site files are owned by a per-site system user.
- Rate limits and lockout on login.

## 6. Repository layout

```
control-portal/
├── panel/     Go: API server, database migrations, job dispatch
├── agent/     Go: server agent, installer script
├── web/       React + Tailwind (Vite) front end
├── docs/      This spec and later design notes
└── README.md
```

Panel and agent share message types through a small Go package so the protocol stays in sync.

## 7. Open questions

1. **TypeScript or plain JavaScript** for the React app? Default: TypeScript.
2. **Hosting the panel**: will it be a hosted service you sell (like Ploi/Cloudways), self-hosted by each user, or both? Default: design for self-hosted first, keep multi-tenant possible.
3. **Product name**: keep "Control Portal" or pick another?
