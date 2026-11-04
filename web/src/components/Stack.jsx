// The web stack on a server: what is installed, and installing more.
import { useState } from 'react'
import { api } from '../api.js'
import { usePolling } from '../usePolling.js'
import { Button, Card, ErrorNote, Spinner } from './ui.jsx'
import { JobLog, componentNames } from './Jobs.jsx'

// Keep in step with protocol/stack.go.
const PHP_VERSIONS = ['7.4', '8.0', '8.1', '8.2', '8.3', '8.4', '8.5']
const NODE_VERSIONS = ['22', '24', '26']

const components = [
  { id: 'nginx', note: 'Web server for your sites' },
  { id: 'php', versions: PHP_VERSIONS, note: 'PHP-FPM with common extensions. Several versions can sit side by side.' },
  { id: 'mysql', note: 'Database server. Pick MySQL or MariaDB.' },
  { id: 'mariadb', note: 'Database server. Pick MySQL or MariaDB.' },
  { id: 'redis', note: 'Cache and queue store' },
  { id: 'nodejs', versions: NODE_VERSIONS, note: 'For building front-end assets, from NodeSource' },
  { id: 'certbot', note: "For free Let's Encrypt certificates" },
  { id: 'composer', note: 'PHP dependencies, used by the default deploy script' },
]

const isActive = (status) => status === 'queued' || status === 'running'

// Same rules as protocol.StackConflict, so the buttons match what the panel allows.
function conflict(id, version, inv) {
  if (id === 'mysql' && inv.mariadb) return 'MariaDB is installed'
  if (id === 'mariadb' && inv.mysql) return 'MySQL is installed'
  if (id === 'nodejs' && inv.nodejs && inv.nodejs.split('.')[0] !== version) return `Node.js ${inv.nodejs} is installed`
  return ''
}

export function StackCard({ serverId, online, inventory }) {
  const { data: jobs, refresh } = usePolling(() => api.jobs(serverId), 3000)
  const [selected, setSelected] = useState(null)
  const [error, setError] = useState('')

  const pending = (jobs ?? []).filter((j) => j.action === 'stack.install' && isActive(j.status))
  const pendingFor = (id, version) =>
    pending.find((j) => j.params?.component === id && (!version || j.params?.version === version))

  const install = async (component, version) => {
    setError('')
    try {
      const job = await api.stackInstall(serverId, component, version)
      setSelected(job.id)
      refresh()
    } catch (err) {
      setError(err.message)
    }
  }

  if (!inventory) {
    return (
      <Card className="p-6 text-sm text-slate-500">
        {online
          ? 'Waiting for the server to report what is installed. If this stays empty, update the Keel agent on the server.'
          : 'The web stack shows here once the server has been online.'}
      </Card>
    )
  }

  return (
    <div className="space-y-4">
      <Card>
        <div className="border-b border-slate-100 px-5 py-3">
          <h2 className="font-medium">Web stack</h2>
          {!online && <p className="mt-1 text-sm text-slate-500">The server is offline. This is what it last reported.</p>}
        </div>
        {error && (
          <div className="px-5 pt-3">
            <ErrorNote>{error}</ErrorNote>
          </div>
        )}
        <ul className="divide-y divide-slate-100">
          {components.map((c) => (
            <ComponentRow
              key={c.id}
              component={c}
              inventory={inventory}
              online={online}
              pendingFor={pendingFor}
              onInstall={install}
              onShowLog={setSelected}
            />
          ))}
        </ul>
      </Card>
      {selected && (
        <Card className="p-5">
          <div className="mb-3 flex items-center">
            <h2 className="font-medium">Install log</h2>
            <button className="ml-auto text-sm text-slate-500 hover:text-slate-900" onClick={() => setSelected(null)}>
              Close
            </button>
          </div>
          <JobLog jobId={selected} />
        </Card>
      )}
    </div>
  )
}

function ComponentRow({ component, inventory, online, pendingFor, onInstall, onShowLog }) {
  const { id, versions, note } = component
  const installed = id === 'php' ? inventory.php : inventory[id] ? [inventory[id]] : []
  const choices = versions ? versions.filter((v) => !(id === 'php' && installed.includes(v))) : []
  const [version, setVersion] = useState(choices[choices.length - 1] ?? '')
  const chosen = choices.includes(version) ? version : choices[choices.length - 1] ?? ''

  // A single-version component that is already installed needs no button.
  const canAdd = id === 'php' ? choices.length > 0 : installed.length === 0
  const blocked = canAdd ? conflict(id, chosen, inventory) : ''
  const job = pendingFor(id, versions ? chosen : '') || (id !== 'php' && pendingFor(id))
  const anyPending = pendingFor(id)

  return (
    <li className="flex flex-wrap items-center gap-x-4 gap-y-2 px-5 py-3">
      <div className="min-w-48 flex-1">
        <div className="font-medium">{componentNames[id]}</div>
        <div className="text-xs text-slate-500">{note}</div>
      </div>
      <div className="flex flex-wrap items-center gap-1.5 text-sm">
        {installed.length === 0 ? (
          <span className="text-slate-400">Not installed</span>
        ) : (
          installed.map((v) => (
            <span key={v} className="rounded-full bg-emerald-50 px-2 py-0.5 text-xs font-medium text-emerald-700">
              {v}
            </span>
          ))
        )}
      </div>
      <div className="flex items-center gap-2 sm:ml-auto">
        {anyPending && (
          <button className="flex items-center gap-2 text-xs text-indigo-700 hover:underline" onClick={() => onShowLog(anyPending.id)}>
            <Spinner />
            {anyPending.status === 'queued' ? 'Queued' : 'Installing'}
            {anyPending.params?.version ? ` ${anyPending.params.version}` : ''}
          </button>
        )}
        {canAdd && (
          <>
            {versions && (
              <select
                className="rounded-md border border-slate-300 bg-white px-2 py-1 text-xs"
                value={chosen}
                onChange={(e) => setVersion(e.target.value)}
                aria-label={`${componentNames[id]} version`}
              >
                {choices.map((v) => (
                  <option key={v} value={v}>
                    {v}
                  </option>
                ))}
              </select>
            )}
            <Button
              variant="secondary"
              className="px-2.5 py-1 text-xs"
              disabled={!online || !!job || !!blocked}
              title={blocked || (!online ? 'The server is offline' : undefined)}
              onClick={() => onInstall(id, versions ? chosen : '')}
            >
              Install
            </Button>
          </>
        )}
      </div>
      {blocked && <p className="w-full text-right text-xs text-slate-500">{blocked}</p>}
    </li>
  )
}
