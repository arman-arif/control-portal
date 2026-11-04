// Deploying a site from GitHub: repository, deploy key, webhook, script and history.
import { useState } from 'react'
import { api } from '../api.js'
import { usePolling } from '../usePolling.js'
import { Button, Card, CopyBox, ErrorNote, Field, Spinner } from './ui.jsx'
import { JobLog, JobStatus } from './Jobs.jsx'
import { timeAgo } from '../format.js'

const repoLink = (url) => url.replace(/^git@github\.com:/, 'https://github.com/').replace(/\.git$/, '')

export function DeploymentsTab({ site, online, onChange }) {
  const { data: deployments, refresh } = usePolling(() => api.deployments(site.id), 2000)
  const [open, setOpen] = useState(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const running = deployments?.find((d) => d.status === 'queued' || d.status === 'running')
  const needsKey = site.repo_url.startsWith('git@') && !site.deploy_key

  const deploy = async () => {
    setError('')
    setBusy(true)
    try {
      const d = await api.deploy(site.id)
      setOpen(d.id)
      refresh()
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-6">
      <RepositoryCard site={site} onChange={onChange} />
      {site.repo_url && (
        <Card>
          <div className="flex flex-wrap items-center gap-3 border-b border-slate-100 px-5 py-3">
            <h2 className="font-medium">Deployments</h2>
            <Button className="ml-auto px-2.5 py-1 text-xs" onClick={deploy} disabled={busy || !online || needsKey || !!running}>
              {(busy || running) && <Spinner />}
              {running ? 'Deploying…' : 'Deploy now'}
            </Button>
          </div>
          {error && (
            <div className="px-5 pt-3">
              <ErrorNote>{error}</ErrorNote>
            </div>
          )}
          {!deployments ? (
            <div className="px-5 py-6">
              <Spinner />
            </div>
          ) : deployments.length === 0 ? (
            <p className="px-5 py-6 text-sm text-slate-500">No deployments yet.</p>
          ) : (
            <ul className="divide-y divide-slate-100">
              {deployments.map((d) => {
                const shown = open === d.id || (open === null && d === running)
                return (
                  <li key={d.id}>
                    <button
                      className={`flex w-full flex-wrap items-center gap-3 px-5 py-3 text-left hover:bg-slate-50 ${shown ? 'bg-slate-50' : ''}`}
                      onClick={() => setOpen(shown ? 0 : d.id)}
                    >
                      <JobStatus status={d.status} />
                      <span className="font-mono text-xs text-slate-500">{d.commit_sha ? d.commit_sha.slice(0, 7) : '—'}</span>
                      <span className="min-w-0 flex-1 truncate text-sm">{d.commit_message || (d.trigger === 'push' ? 'Push' : 'Manual deploy')}</span>
                      <span className="text-xs text-slate-500">
                        {d.trigger === 'push' ? 'push' : 'manual'} · {timeAgo(d.created_at)}
                      </span>
                    </button>
                    {shown && d.job_id && (
                      <div className="px-5 pb-4">
                        <JobLog jobId={d.job_id} />
                      </div>
                    )}
                  </li>
                )
              })}
            </ul>
          )}
        </Card>
      )}
      {site.repo_url && <ScriptCard key={site.deploy_script + site.deploy_timeout} site={site} onChange={onChange} />}
    </div>
  )
}

function RepositoryCard({ site, onChange }) {
  const { data: app } = usePolling(() => api.githubApp(), 60000)
  const [useApp, setUseApp] = useState(site.github_installation_id != null)
  const [appRepos, setAppRepos] = useState(null)
  const [repo, setRepo] = useState(site.repo_url)
  const [branch, setBranch] = useState(site.branch || 'main')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [secret, setSecret] = useState('')
  const appRepoName = (url) => url.replace(/^https:\/\/github\.com\//, '').replace(/\.git$/, '')
  const picked = useApp ? appRepos?.find((r) => r.full_name === appRepoName(repo)) : null
  const changed =
    repo.trim() !== site.repo_url || branch.trim() !== site.branch || useApp !== (site.github_installation_id != null)

  const chooseApp = async (on) => {
    setUseApp(on)
    setError('')
    if (on && !appRepos) {
      try {
        setAppRepos(await api.githubAppRepos())
      } catch (err) {
        setError(err.message)
      }
    }
  }

  const save = async (e) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      const v = useApp
        ? await api.setRepository(site.id, repo.trim(), branch.trim(), picked?.installation_id ?? site.github_installation_id)
        : await api.setRepository(site.id, repo.trim(), branch.trim())
      setRepo(v.repo_url)
      setBranch(v.branch)
      onChange()
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }
  const showSecret = async () => {
    try {
      setSecret((await api.webhookSecret(site.id)).secret)
    } catch (err) {
      setError(err.message)
    }
  }
  const ssh = site.repo_url.startsWith('git@')

  return (
    <Card>
      <form onSubmit={save} className="space-y-4 px-5 py-4">
        <h2 className="font-medium">Repository</h2>
        {app?.configured && (
          <div className="flex flex-wrap gap-4 text-sm">
            <label className="flex items-center gap-2">
              <input type="radio" checked={!useApp} onChange={() => chooseApp(false)} />
              Deploy key or public repository
            </label>
            <label className="flex items-center gap-2">
              <input type="radio" checked={useApp} onChange={() => chooseApp(true)} />
              Through the GitHub App ({app.app.name})
            </label>
          </div>
        )}
        <div className="grid gap-4 sm:grid-cols-[2fr_1fr]">
          {useApp ? (
            <label className="block">
              <span className="mb-1 block text-sm font-medium text-slate-700">Repository</span>
              <select
                className="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm"
                value={appRepoName(repo)}
                onChange={(e) => {
                  const r = appRepos.find((x) => x.full_name === e.target.value)
                  setRepo(`https://github.com/${r.full_name}.git`)
                  setBranch(r.default_branch)
                }}
                required
              >
                <option value="">{appRepos ? 'Pick a repository' : 'Loading…'}</option>
                {(appRepos ?? []).map((r) => (
                  <option key={r.full_name} value={r.full_name}>
                    {r.full_name}
                    {r.private ? ' (private)' : ''}
                  </option>
                ))}
                {!appRepos && site.github_installation_id != null && <option value={appRepoName(site.repo_url)}>{appRepoName(site.repo_url)}</option>}
              </select>
            </label>
          ) : (
            <Field label="GitHub repository" placeholder="git@github.com:owner/repo.git" value={repo} onChange={(e) => setRepo(e.target.value)} required />
          )}
          <Field label="Branch" placeholder="main" value={branch} onChange={(e) => setBranch(e.target.value)} />
        </div>
        <p className="text-xs text-slate-500">
          {useApp
            ? "Keel gets a short-lived, read-only token for the repository at each deploy. Pushes reach Keel through the app's webhook."
            : 'Use the SSH address for a private repository; the server gets its own deploy key for it. An https://github.com address works for public repositories without a key.'}
        </p>
        <ErrorNote>{error}</ErrorNote>
        <Button type="submit" disabled={busy || !changed}>
          {busy && <Spinner />}
          Save repository
        </Button>
      </form>

      {site.repo_url && site.github_installation_id == null && (
        <div className="space-y-5 border-t border-slate-100 px-5 py-4">
          {ssh && (
            <div className="space-y-2">
              <h3 className="text-sm font-medium">1. Deploy key</h3>
              {site.deploy_key ? (
                <>
                  <p className="text-sm text-slate-600">
                    In{' '}
                    <a href={`${repoLink(site.repo_url)}/settings/keys/new`} target="_blank" rel="noreferrer" className="text-indigo-600 hover:underline">
                      the repository's deploy keys
                    </a>
                    , add this key. Leave write access off.
                  </p>
                  <CopyBox text={site.deploy_key} />
                </>
              ) : (
                <p className="flex items-center gap-2 text-sm text-slate-500">
                  <Spinner /> The server is making the key…
                </p>
              )}
            </div>
          )}
          <div className="space-y-2">
            <h3 className="text-sm font-medium">{ssh ? '2. ' : ''}Deploy on push (optional)</h3>
            <p className="text-sm text-slate-600">
              In{' '}
              <a href={`${repoLink(site.repo_url)}/settings/hooks/new`} target="_blank" rel="noreferrer" className="text-indigo-600 hover:underline">
                the repository's webhooks
              </a>
              , add this payload URL with content type <code className="font-mono">application/json</code>, the secret below, and just the
              push event. Pushes to <code className="font-mono">{site.branch}</code> then deploy automatically. GitHub must be able to reach
              this panel.
            </p>
            <CopyBox text={site.webhook_url} />
            {secret ? (
              <CopyBox text={secret} />
            ) : (
              <Button variant="secondary" className="px-2.5 py-1 text-xs" onClick={showSecret}>
                Show secret
              </Button>
            )}
          </div>
        </div>
      )}
    </Card>
  )
}

function ScriptCard({ site, onChange }) {
  const [script, setScript] = useState(site.deploy_script)
  const [timeout, setTimeoutMinutes] = useState(String(site.deploy_timeout))
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const changed = script !== site.deploy_script || Number(timeout) !== site.deploy_timeout

  const save = async (reset) => {
    setError('')
    setBusy(true)
    try {
      await api.setDeployScript(site.id, reset ? null : script, Number(timeout))
      onChange()
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          save(false)
        }}
        className="space-y-3 px-5 py-4"
      >
        <h2 className="font-medium">Deploy script</h2>
        <p className="text-sm text-slate-600">
          Runs with <code className="font-mono">bash -e</code> as {site.system_user} in {site.dir}, after the code is checked out. Your{' '}
          <code className="font-mono">.env</code> file is left alone. <code className="font-mono">php</code> is PHP {site.php_version}.
        </p>
        <textarea
          className="block h-64 w-full rounded-md border border-slate-300 bg-slate-900 px-3 py-2 font-mono text-xs text-slate-100 shadow-sm"
          value={script}
          onChange={(e) => setScript(e.target.value)}
          spellCheck={false}
        />
        <div className="w-40">
          <Field label="Timeout (minutes)" type="number" min="1" max="180" value={timeout} onChange={(e) => setTimeoutMinutes(e.target.value)} />
        </div>
        <ErrorNote>{error}</ErrorNote>
        <div className="flex gap-2">
          <Button type="submit" disabled={busy || !changed}>
            Save script
          </Button>
          {!site.default_deploy_script && (
            <Button type="button" variant="secondary" disabled={busy} onClick={() => save(true)}>
              Use Keel's default
            </Button>
          )}
        </div>
      </form>
    </Card>
  )
}
