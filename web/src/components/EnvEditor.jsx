// The site's .env file, read from and saved straight to the server.
import { useEffect, useState } from 'react'
import { api } from '../api.js'
import { Button, Card, ErrorNote, Spinner } from './ui.jsx'

export function EnvTab({ site, online }) {
  const [file, setFile] = useState(null) // {exists, content} as last loaded or saved
  const [text, setText] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [saved, setSaved] = useState(false)

  const load = async () => {
    setError('')
    setSaved(false)
    try {
      const f = await api.siteEnv(site.id)
      setFile(f)
      setText(f.content)
    } catch (err) {
      setError(err.message)
    }
  }
  useEffect(() => {
    if (online) load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [site.id, online])

  const save = async (e) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      await api.saveSiteEnv(site.id, text)
      setFile({ exists: true, content: text })
      setSaved(true)
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  const changed = file && text !== file.content

  return (
    <Card>
      <form onSubmit={save} className="space-y-3 px-5 py-4">
        <div className="flex items-center gap-3">
          <h2 className="font-medium">Environment</h2>
          {online && (
            <button type="button" className="ml-auto text-sm text-slate-500 hover:text-slate-900" onClick={load}>
              Reload from server
            </button>
          )}
        </div>
        <p className="text-sm text-slate-600">
          The <code className="font-mono">.env</code> file in {site.dir}. It is read from and saved straight to the server, readable only by{' '}
          {site.system_user}; Keel keeps no copy. Deploys leave it alone.
        </p>
        {!online ? (
          <p className="rounded-md bg-amber-50 px-3 py-2 text-sm text-amber-800">The server is offline. The .env file can only be edited while it is online.</p>
        ) : !file && !error ? (
          <Spinner />
        ) : (
          <>
            {file && !file.exists && <p className="text-sm text-slate-500">There is no .env file yet. Saving creates it.</p>}
            <textarea
              className="block h-96 w-full rounded-md border border-slate-300 bg-slate-900 px-3 py-2 font-mono text-xs text-slate-100 shadow-sm"
              value={text}
              onChange={(e) => {
                setText(e.target.value)
                setSaved(false)
              }}
              spellCheck={false}
              autoComplete="off"
              disabled={!file}
            />
          </>
        )}
        <ErrorNote>{error}</ErrorNote>
        <div className="flex items-center gap-3">
          <Button type="submit" disabled={!online || busy || !changed}>
            {busy && <Spinner />}
            Save .env
          </Button>
          {saved && !changed && <span className="text-sm text-emerald-700">Saved on the server.</span>}
        </div>
      </form>
    </Card>
  )
}
