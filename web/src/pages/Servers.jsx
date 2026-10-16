import { Link } from 'react-router-dom'
import { api } from '../api.js'
import { usePolling } from '../usePolling.js'
import { Button, Card, ErrorNote, Meter, Spinner, StatusDot } from '../components/ui.jsx'
import { percent, timeAgo } from '../format.js'

export default function Servers() {
  const { data: servers, error } = usePolling(api.servers, 5000)

  return (
    <div>
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-2xl font-semibold tracking-tight">Servers</h1>
        <Link to="/servers/new">
          <Button>Add server</Button>
        </Link>
      </div>
      <ErrorNote>{error?.message}</ErrorNote>
      {!servers && !error && <Spinner />}
      {servers?.length === 0 && (
        <Card className="p-10 text-center">
          <h2 className="font-medium">No servers yet</h2>
          <p className="mt-1 text-sm text-slate-500">Link a fresh Ubuntu server with a single command.</p>
          <Link to="/servers/new" className="mt-4 inline-block">
            <Button>Add your first server</Button>
          </Link>
        </Card>
      )}
      {servers?.length > 0 && (
        <Card className="divide-y divide-slate-100">
          {servers.map((s) => (
            <Link key={s.id} to={`/servers/${s.id}`} className="grid grid-cols-12 items-center gap-4 px-5 py-4 hover:bg-slate-50">
              <div className="col-span-12 sm:col-span-4">
                <div className="font-medium">{s.name}</div>
                <div className="text-sm text-slate-500">
                  {s.linked ? [s.ip, s.os].filter(Boolean).join(' · ') || s.hostname : 'Waiting for install'}
                </div>
              </div>
              <div className="col-span-4 sm:col-span-2">
                {s.linked ? <StatusDot online={s.online} /> : <span className="text-sm text-amber-600">Not linked</span>}
                {s.linked && !s.online && <div className="text-xs text-slate-400">seen {timeAgo(s.last_seen_at)}</div>}
              </div>
              <MiniStat label="CPU" value={s.latest?.cpu_percent} />
              <MiniStat label="Memory" value={s.latest && percent(s.latest.mem_used, s.latest.mem_total)} />
              <MiniStat label="Disk" value={s.latest && percent(s.latest.disk_used, s.latest.disk_total)} />
            </Link>
          ))}
        </Card>
      )}
    </div>
  )
}

function MiniStat({ label, value }) {
  return (
    <div className="col-span-8 hidden sm:col-span-2 sm:block">
      <div className="mb-1 flex justify-between text-xs text-slate-500">
        <span>{label}</span>
        <span>{value == null ? '—' : `${Math.round(value)}%`}</span>
      </div>
      <Meter value={value} />
    </div>
  )
}
