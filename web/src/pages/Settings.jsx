import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api } from '../api.js'
import { usePolling } from '../usePolling.js'
import { Button, Card, ErrorNote, Field, Spinner } from '../components/ui.jsx'

export default function Settings() {
  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold tracking-tight">Settings</h1>
      <GitHubAppCard />
    </div>
  )
}

function GitHubAppCard() {
  const { data, error, refresh } = usePolling(() => api.githubApp(), 15000)
  const [params, setParams] = useSearchParams()
  const [org, setOrg] = useState('')
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState('')

  // GitHub's manifest flow needs a form posted from the browser to github.com.
  const create = async (e) => {
    e.preventDefault()
    setActionError('')
    setBusy(true)
    try {
      const { action, manifest } = await api.githubAppManifest(org.trim())
      const form = document.createElement('form')
      form.method = 'post'
      form.action = action
      const input = document.createElement('input')
      input.type = 'hidden'
      input.name = 'manifest'
      input.value = manifest
      form.appendChild(input)
      document.body.appendChild(form)
      form.submit()
    } catch (err) {
      setActionError(err.message)
      setBusy(false)
    }
  }

  const remove = async () => {
    if (!confirm('Remove the GitHub App from Keel? Sites that deploy through it stop deploying until you pick another source.')) return
    try {
      await api.deleteGitHubApp()
      refresh()
    } catch (err) {
      setActionError(err.message)
    }
  }

  const notice = params.get('github') === 'created' ? 'The GitHub App was created. Now install it on the repositories you deploy.' : ''
  const failure = params.get('github_error') || ''
  const dismiss = () => setParams({}, { replace: true })

  return (
    <Card>
      <div className="border-b border-slate-100 px-5 py-3">
        <h2 className="font-medium">GitHub App</h2>
      </div>
      <div className="space-y-4 px-5 py-4">
        {notice && (
          <p className="flex rounded-md bg-emerald-50 px-3 py-2 text-sm text-emerald-800">
            {notice}
            <button className="ml-auto text-emerald-700" onClick={dismiss}>
              ×
            </button>
          </p>
        )}
        <ErrorNote>{failure || actionError || error?.message}</ErrorNote>
        {!data ? (
          <Spinner />
        ) : !data.configured ? (
          <form onSubmit={create} className="space-y-3">
            <p className="text-sm text-slate-600">
              A GitHub App lets sites deploy private repositories without a deploy key per site, and sends pushes to Keel through one
              webhook. Keel creates the app on GitHub for you: it can only read code. This is optional; deploy keys keep working.
            </p>
            <div className="max-w-xs">
              <Field label="GitHub organization (optional)" placeholder="Leave empty for your own account" value={org} onChange={(e) => setOrg(e.target.value)} />
            </div>
            <Button type="submit" disabled={busy}>
              {busy && <Spinner />}
              Create GitHub App
            </Button>
          </form>
        ) : (
          <div className="space-y-3 text-sm">
            <p>
              <a href={data.app.html_url} target="_blank" rel="noreferrer" className="font-medium text-indigo-600 hover:underline">
                {data.app.name}
              </a>{' '}
              <span className="text-slate-500">(app {data.app.app_id})</span>
            </p>
            {data.error ? (
              <ErrorNote>Could not reach GitHub: {data.error}</ErrorNote>
            ) : data.installations?.length ? (
              <p className="text-slate-600">Installed on {data.installations.map((i) => i.account.login).join(', ')}.</p>
            ) : (
              <p className="text-slate-600">Not installed anywhere yet.</p>
            )}
            <div className="flex flex-wrap gap-2">
              <a href={data.install_url} target="_blank" rel="noreferrer">
                <Button type="button" variant="secondary">
                  Install on repositories
                </Button>
              </a>
              <Button type="button" variant="danger" onClick={remove}>
                Remove from Keel
              </Button>
            </div>
            <p className="text-xs text-slate-500">Removing it here doesn't delete it on GitHub; do that in the app's settings there.</p>
          </div>
        )}
      </div>
    </Card>
  )
}
