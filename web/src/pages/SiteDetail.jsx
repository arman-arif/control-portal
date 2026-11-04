import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api } from '../api.js'
import { usePolling } from '../usePolling.js'
import { Button, Card, ErrorNote, Field, Spinner, StatusDot } from '../components/ui.jsx'
import { JobLog, JobStatus, jobLabel } from '../components/Jobs.jsx'
import { SiteStatus, parseNames, sitePending } from '../components/Sites.jsx'
import { timeAgo } from '../format.js'

export default function SiteDetail() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { data: site, error, refresh } = usePolling(() => api.site(id), 2000)
  const { data: server } = usePolling(() => (site ? api.server(site.server_id) : Promise.resolve(null)), 5000)
  const { data: stats } = usePolling(() => (site ? api.stats(site.server_id) : Promise.resolve(null)), 5000)
  const [openJob, setOpenJob] = useState(null)

  // Once a delete has gone through, the site is gone: go back to its server.
  const [lastServer, setLastServer] = useState(null)
  useEffect(() => {
    if (site) setLastServer(site.server_id)
  }, [site])
  useEffect(() => {
    if (error?.status === 404 && lastServer) navigate(`/servers/${lastServer}?tab=sites`, { replace: true })
  }, [error, lastServer, navigate])

  if (error?.status === 404 && !lastServer) {
    return (
      <Card className="p-10 text-center">
        <p>This site does not exist.</p>
        <Link to="/servers" className="mt-3 inline-block text-sm text-indigo-600 hover:underline">
          Back to servers
        </Link>
      </Card>
    )
  }
  if (!site) return error ? <ErrorNote>{error.message}</ErrorNote> : <Spinner />

  const latestJob = site.jobs[0]
  const shownJob = openJob ?? (latestJob && (sitePending(site.status) || site.status === 'failed') ? latestJob.id : null)

  return (
    <div className="space-y-6">
      <div>
        <Link to={`/servers/${site.server_id}?tab=sites`} className="text-sm text-slate-500 hover:text-slate-900">
          ← {server?.name ?? 'Server'}
        </Link>
        <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-2">
          <h1 className="text-2xl font-semibold tracking-tight">{site.domain}</h1>
          <SiteStatus status={site.status} />
          {server && <StatusDot online={server.online} />}
        </div>
        {site.status === 'failed' && site.error && <p className="mt-2 text-sm text-red-600">{site.error}</p>}
      </div>

      <Card>
        <dl className="grid gap-x-6 gap-y-3 px-5 py-4 text-sm sm:grid-cols-[10rem_1fr]">
          <dt className="text-slate-500">Domain</dt>
          <dd>
            <a href={`http://${site.domain}`} target="_blank" rel="noreferrer" className="text-indigo-600 hover:underline">
              {site.domain}
            </a>
            {site.aliases.length > 0 && <span className="text-slate-500"> · {site.aliases.join(', ')}</span>}
          </dd>
          <dt className="text-slate-500">PHP</dt>
          <dd>{site.php_version}</dd>
          <dt className="text-slate-500">System user</dt>
          <dd className="font-mono">{site.system_user}</dd>
          <dt className="text-slate-500">Site folder</dt>
          <dd className="font-mono break-all">{site.dir}</dd>
          <dt className="text-slate-500">Web root</dt>
          <dd className="font-mono break-all">{site.root}</dd>
        </dl>
      </Card>

      <SettingsCard key={`${site.php_version}|${site.web_dir}|${site.aliases.join(',')}`} site={site} inventory={stats?.inventory} onSaved={refresh} />

      <Card>
        <div className="border-b border-slate-100 px-5 py-3">
          <h2 className="font-medium">Activity</h2>
        </div>
        {site.jobs.length === 0 ? (
          <p className="px-5 py-6 text-sm text-slate-500">Nothing yet.</p>
        ) : (
          <ul className="divide-y divide-slate-100">
            {site.jobs.map((j) => (
              <li key={j.id}>
                <button
                  className={`flex w-full flex-wrap items-center gap-3 px-5 py-3 text-left hover:bg-slate-50 ${shownJob === j.id ? 'bg-slate-50' : ''}`}
                  onClick={() => setOpenJob(shownJob === j.id ? 0 : j.id)}
                >
                  <JobStatus status={j.status} />
                  <span className="text-sm">{jobLabel(j.action, j.params)}</span>
                  <span className="ml-auto text-xs text-slate-500">{timeAgo(j.created_at)}</span>
                </button>
                {shownJob === j.id && (
                  <div className="px-5 pb-4">
                    <JobLog jobId={j.id} />
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </Card>

      <DeleteCard site={site} onDeleted={refresh} />
    </div>
  )
}

function SettingsCard({ site, inventory, onSaved }) {
  const [php, setPhp] = useState(site.php_version)
  const [aliases, setAliases] = useState(site.aliases.join(', '))
  const [webDir, setWebDir] = useState(site.web_dir)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const versions = [...new Set([...(inventory?.php ?? []), site.php_version])].sort()
  const changed = php !== site.php_version || webDir !== site.web_dir || parseNames(aliases).join(',') !== site.aliases.join(',')

  const save = async (e) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      await api.updateSite(site.id, { php_version: php, aliases: parseNames(aliases), web_dir: webDir })
      onSaved()
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <form onSubmit={save} className="space-y-4 px-5 py-4">
        <h2 className="font-medium">Settings</h2>
        <div className="grid gap-4 sm:grid-cols-3">
          <label className="block">
            <span className="mb-1 block text-sm font-medium text-slate-700">PHP version</span>
            <select
              className="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm"
              value={php}
              onChange={(e) => setPhp(e.target.value)}
            >
              {versions.map((v) => (
                <option key={v} value={v}>
                  PHP {v}
                </option>
              ))}
            </select>
          </label>
          <Field label="Aliases" placeholder="www.example.com" value={aliases} onChange={(e) => setAliases(e.target.value)} />
          <Field label="Web directory" placeholder="public" value={webDir} onChange={(e) => setWebDir(e.target.value)} />
        </div>
        <ErrorNote>{error}</ErrorNote>
        <Button type="submit" disabled={busy || !changed || site.status === 'deleting'}>
          {busy && <Spinner />}
          Save changes
        </Button>
      </form>
    </Card>
  )
}

function DeleteCard({ site, onDeleted }) {
  const [deleteFiles, setDeleteFiles] = useState(false)
  const [confirmText, setConfirmText] = useState('')
  const [error, setError] = useState('')

  const remove = async (e) => {
    e.preventDefault()
    setError('')
    try {
      await api.deleteSite(site.id, deleteFiles)
      onDeleted()
    } catch (err) {
      setError(err.message)
    }
  }

  return (
    <Card className="border-red-200">
      <form onSubmit={remove} className="space-y-3 px-5 py-4">
        <h2 className="font-medium text-red-700">Delete site</h2>
        <p className="text-sm text-slate-600">
          Removes the site's Nginx config and PHP-FPM pool, so the server stops serving it. The files in {site.dir} stay unless you choose
          otherwise.
        </p>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={deleteFiles} onChange={(e) => setDeleteFiles(e.target.checked)} />
          Also delete the system user {site.system_user} and all files in {`/home/${site.system_user}`}
        </label>
        <Field
          label={`Type ${site.domain} to confirm`}
          value={confirmText}
          onChange={(e) => setConfirmText(e.target.value)}
          autoComplete="off"
        />
        <ErrorNote>{error}</ErrorNote>
        <Button variant="danger" type="submit" disabled={confirmText.trim() !== site.domain || site.status === 'deleting'}>
          {site.status === 'deleting' ? 'Deleting…' : 'Delete site'}
        </Button>
      </form>
    </Card>
  )
}
