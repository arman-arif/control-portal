import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api } from '../api.js'
import { usePolling } from '../usePolling.js'
import { Button, Card, ErrorNote, Meter, Sparkline, Spinner, StatusDot } from '../components/ui.jsx'
import InstallSteps from '../components/InstallSteps.jsx'
import { formatBytes, formatUptime, percent, timeAgo } from '../format.js'

export default function ServerDetail() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { data: server, error } = usePolling(() => api.server(id), 5000)
  const { data: stats, refresh: refreshStats } = usePolling(() => api.stats(id), 5000)

  if (error?.status === 404) {
    return (
      <Card className="p-10 text-center">
        <p>This server does not exist.</p>
        <Link to="/servers" className="mt-3 inline-block text-sm text-indigo-600 hover:underline">
          Back to servers
        </Link>
      </Card>
    )
  }
  if (!server) return error ? <ErrorNote>{error.message}</ErrorNote> : <Spinner />

  const remove = async () => {
    if (!confirm(`Remove ${server.name} from Keel? The agent on the server will be disconnected.`)) return
    try {
      await api.deleteServer(server.id)
      navigate('/servers')
    } catch (err) {
      alert(err.message)
    }
  }

  const history = stats?.history ?? []
  const latest = stats?.online ? history[history.length - 1] : null

  return (
    <div className="space-y-6">
      <div>
        <Link to="/servers" className="text-sm text-slate-500 hover:text-slate-900">
          ← Servers
        </Link>
        <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-2">
          <h1 className="text-2xl font-semibold tracking-tight">{server.name}</h1>
          {server.linked && <StatusDot online={server.online} />}
          <Button variant="danger" className="ml-auto" onClick={remove}>
            Remove server
          </Button>
        </div>
        {server.linked && (
          <p className="mt-1 text-sm text-slate-500">
            {[server.ip, server.hostname, server.os, server.cpus && `${server.cpus} CPU`, server.arch].filter(Boolean).join(' · ')}
            {!server.online && ` · last seen ${timeAgo(server.last_seen_at)}`}
          </p>
        )}
      </div>

      {!server.linked ? (
        <PendingInstall serverId={server.id} />
      ) : (
        <>
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <StatCard label="CPU" value={latest && `${latest.cpu_percent.toFixed(0)}%`} series={history.map((s) => s.cpu_percent)} />
            <StatCard
              label="Memory"
              value={latest && `${formatBytes(latest.mem_used)} / ${formatBytes(latest.mem_total)}`}
              series={history.map((s) => percent(s.mem_used, s.mem_total))}
            />
            <StatCard
              label="Disk"
              value={latest && `${formatBytes(latest.disk_used)} / ${formatBytes(latest.disk_total)}`}
              meter={latest && percent(latest.disk_used, latest.disk_total)}
            />
            <Card className="p-4">
              <div className="text-sm text-slate-500">Load average</div>
              <div className="mt-1 text-xl font-semibold">
                {latest ? `${latest.load1.toFixed(2)} ${latest.load5.toFixed(2)} ${latest.load15.toFixed(2)}` : '—'}
              </div>
              <div className="mt-4 text-sm text-slate-500">Uptime</div>
              <div className="mt-1 font-medium">{latest ? formatUptime(latest.uptime) : '—'}</div>
            </Card>
          </div>
          <Services serverId={server.id} online={server.online} services={stats?.services ?? []} onChange={refreshStats} />
        </>
      )}
    </div>
  )
}

function StatCard({ label, value, series, meter }) {
  return (
    <Card className="p-4">
      <div className="text-sm text-slate-500">{label}</div>
      <div className="mt-1 text-xl font-semibold">{value || '—'}</div>
      {series && <Sparkline values={series} className="mt-3" />}
      {meter != null && (
        <div className="mt-6">
          <Meter value={meter} />
          <div className="mt-1 text-xs text-slate-500">{Math.round(meter)}% used</div>
        </div>
      )}
    </Card>
  )
}

function PendingInstall({ serverId }) {
  const [install, setInstall] = useState(null)
  const [error, setError] = useState('')
  const generate = async () => {
    setError('')
    try {
      setInstall(await api.installCommand(serverId))
    } catch (err) {
      setError(err.message)
    }
  }
  return (
    <Card className="p-6">
      {install ? (
        <InstallSteps install={install} />
      ) : (
        <div className="space-y-3">
          <h2 className="font-medium">This server is not linked yet</h2>
          <p className="text-sm text-slate-500">
            Run the install command on the server. If you lost it or it expired, get a new one. The old command stops working.
          </p>
          <Button onClick={generate}>Get install command</Button>
        </div>
      )}
      <div className="mt-3">
        <ErrorNote>{error}</ErrorNote>
      </div>
    </Card>
  )
}

function Services({ serverId, online, services, onChange }) {
  const [busy, setBusy] = useState(null)
  const [message, setMessage] = useState(null)

  const run = async (name, action) => {
    setBusy(`${name}:${action}`)
    setMessage(null)
    try {
      await api.serviceAction(serverId, name, action)
      setMessage({ ok: true, text: `${name}: ${action} done` })
    } catch (err) {
      setMessage({ ok: false, text: `${name}: ${err.message}` })
    } finally {
      setBusy(null)
      onChange()
    }
  }

  return (
    <Card>
      <div className="flex items-center justify-between border-b border-slate-100 px-5 py-3">
        <h2 className="font-medium">Services</h2>
        {message && <span className={`text-sm ${message.ok ? 'text-emerald-700' : 'text-red-600'}`}>{message.text}</span>}
      </div>
      {services.length === 0 ? (
        <p className="px-5 py-6 text-sm text-slate-500">
          {online ? 'No managed services found on this server yet.' : 'Services appear when the server is online.'}
        </p>
      ) : (
        <ul className="divide-y divide-slate-100">
          {services.map((s) => {
            const running = s.active === 'active'
            return (
              <li key={s.name} className="flex flex-wrap items-center gap-3 px-5 py-3">
                <span className={`h-2 w-2 rounded-full ${running ? 'bg-emerald-500' : s.active === 'failed' ? 'bg-red-500' : 'bg-slate-300'}`} />
                <span className="font-mono text-sm">{s.name}</span>
                <span className="text-sm text-slate-500">
                  {s.active} ({s.sub})
                </span>
                <div className="ml-auto flex gap-2">
                  {['start', 'restart', 'stop'].map((action) => (
                    <Button
                      key={action}
                      variant={action === 'stop' ? 'danger' : 'secondary'}
                      className="px-2.5 py-1 text-xs capitalize"
                      disabled={!online || busy !== null || (action === 'start' && running) || (action === 'stop' && !running)}
                      onClick={() => run(s.name, action)}
                    >
                      {busy === `${s.name}:${action}` && <Spinner />}
                      {action}
                    </Button>
                  ))}
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </Card>
  )
}
