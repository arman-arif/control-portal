// The sites on a server: the list and the form for a new one.
import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api } from '../api.js'
import { usePolling } from '../usePolling.js'
import { Button, Card, ErrorNote, Field, Spinner } from './ui.jsx'

const statusStyles = {
  creating: 'bg-indigo-50 text-indigo-700',
  updating: 'bg-indigo-50 text-indigo-700',
  deleting: 'bg-amber-50 text-amber-700',
  active: 'bg-emerald-50 text-emerald-700',
  failed: 'bg-red-50 text-red-700',
}

export const sitePending = (status) => status === 'creating' || status === 'updating' || status === 'deleting'

export function SiteStatus({ status }) {
  return (
    <span className={`inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ${statusStyles[status] || ''}`}>
      {sitePending(status) && <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-current" />}
      {status}
    </span>
  )
}

// Splits what a person typed into separate names, like protocol.ParseAliases.
export const parseNames = (text) => [...new Set(text.toLowerCase().split(/[\s,]+/).filter(Boolean))]

export function SitesCard({ serverId, online, inventory }) {
  const { data: sites, error, refresh } = usePolling(() => api.sites(serverId), 3000)
  const [adding, setAdding] = useState(false)

  return (
    <div className="space-y-4">
      <Card>
        <div className="flex items-center border-b border-slate-100 px-5 py-3">
          <h2 className="font-medium">Sites</h2>
          {!adding && (
            <Button className="ml-auto px-2.5 py-1 text-xs" onClick={() => setAdding(true)}>
              New site
            </Button>
          )}
        </div>
        {adding && <NewSiteForm serverId={serverId} online={online} inventory={inventory} onCancel={() => setAdding(false)} onCreated={refresh} />}
        {error && (
          <div className="px-5 pt-3">
            <ErrorNote>{error.message}</ErrorNote>
          </div>
        )}
        {!sites ? (
          <div className="px-5 py-6">
            <Spinner />
          </div>
        ) : sites.length === 0 ? (
          <p className="px-5 py-6 text-sm text-slate-500">No sites on this server yet.</p>
        ) : (
          <ul className="divide-y divide-slate-100">
            {sites.map((s) => (
              <li key={s.id}>
                <Link to={`/sites/${s.id}`} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-5 py-3 hover:bg-slate-50">
                  <span className="font-medium">{s.domain}</span>
                  {s.aliases.length > 0 && <span className="text-xs text-slate-500">+{s.aliases.length} more</span>}
                  <span className="text-sm text-slate-500">PHP {s.php_version}</span>
                  <span className="ml-auto">
                    <SiteStatus status={s.status} />
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  )
}

function NewSiteForm({ serverId, online, inventory, onCancel, onCreated }) {
  const navigate = useNavigate()
  const phpVersions = inventory?.php ?? []
  const [domain, setDomain] = useState('')
  const [aliases, setAliases] = useState('')
  const [webDir, setWebDir] = useState('public')
  const [php, setPhp] = useState(phpVersions[phpVersions.length - 1] ?? '')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const missing = !inventory
    ? ''
    : !inventory.nginx
      ? 'Install Nginx from the Stack tab first.'
      : phpVersions.length === 0
        ? 'Install a PHP version from the Stack tab first.'
        : ''

  const submit = async (e) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      const res = await api.createSite(serverId, { domain: domain.trim(), aliases: parseNames(aliases), web_dir: webDir, php_version: php })
      onCreated()
      navigate(`/sites/${res.site.id}`)
    } catch (err) {
      setError(err.message)
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4 border-b border-slate-100 px-5 py-4">
      {missing && <p className="rounded-md bg-amber-50 px-3 py-2 text-sm text-amber-800">{missing}</p>}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Domain" placeholder="example.com" value={domain} onChange={(e) => setDomain(e.target.value)} required autoFocus />
        <Field
          label="Aliases (optional)"
          placeholder="www.example.com"
          value={aliases}
          onChange={(e) => setAliases(e.target.value)}
        />
        <label className="block">
          <span className="mb-1 block text-sm font-medium text-slate-700">PHP version</span>
          <select
            className="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm"
            value={php}
            onChange={(e) => setPhp(e.target.value)}
            required
          >
            {phpVersions.length === 0 && <option value="">None installed</option>}
            {phpVersions.map((v) => (
              <option key={v} value={v}>
                PHP {v}
              </option>
            ))}
          </select>
        </label>
        <Field label="Web directory" placeholder="public" value={webDir} onChange={(e) => setWebDir(e.target.value)} />
      </div>
      <p className="text-xs text-slate-500">
        The site gets its own system user, a PHP-FPM pool and an Nginx config. The web directory is served from inside the site's folder;
        leave it empty to serve the folder itself.
      </p>
      <ErrorNote>{error}</ErrorNote>
      <div className="flex gap-2">
        <Button type="submit" disabled={busy || !online || !!missing || !php}>
          {busy && <Spinner />}
          Create site
        </Button>
        <Button type="button" variant="secondary" onClick={onCancel}>
          Cancel
        </Button>
        {!online && <span className="self-center text-sm text-slate-500">The server is offline.</span>}
      </div>
    </form>
  )
}
