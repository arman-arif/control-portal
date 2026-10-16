import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api } from '../api.js'
import { Button, Card, ErrorNote, Field, Spinner } from '../components/ui.jsx'
import InstallSteps from '../components/InstallSteps.jsx'

export default function AddServer() {
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [created, setCreated] = useState(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      setCreated(await api.createServer(name))
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto max-w-2xl">
      <Link to="/servers" className="text-sm text-slate-500 hover:text-slate-900">
        ← Servers
      </Link>
      <h1 className="mt-2 mb-6 text-2xl font-semibold tracking-tight">Add a server</h1>

      {!created ? (
        <Card className="p-6">
          <form onSubmit={submit} className="space-y-4">
            <Field
              label="Server name"
              placeholder="e.g. web-production"
              maxLength={64}
              required
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            <p className="text-sm text-slate-500">
              Use a fresh Ubuntu 22.04 or 24.04 server. You will get a command to run on it next.
            </p>
            <ErrorNote>{error}</ErrorNote>
            <Button type="submit" disabled={busy}>
              {busy && <Spinner />}
              Continue
            </Button>
          </form>
        </Card>
      ) : (
        <Card className="p-6">
          <InstallSteps install={created.install} />
          <div className="mt-6 flex gap-3">
            <Button onClick={() => navigate(`/servers/${created.server.id}`)}>Go to server</Button>
          </div>
        </Card>
      )}
    </div>
  )
}
