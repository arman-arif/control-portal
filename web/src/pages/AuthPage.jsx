import { useEffect, useState } from 'react'
import { api } from '../api.js'
import { useAuth } from '../App.jsx'
import { Button, Card, ErrorNote, Field, Spinner } from '../components/ui.jsx'

export default function AuthPage() {
  const { setUser } = useAuth()
  const [status, setStatus] = useState(null)
  const [mode, setMode] = useState('login')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    api
      .authStatus()
      .then((s) => {
        setStatus(s)
        if (s.needs_setup) setMode('signup')
      })
      .catch(() => setStatus({ signup_open: false, needs_setup: false }))
  }, [])

  const submit = async (e) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      const user = mode === 'signup' ? await api.signup(email, password) : await api.login(email, password)
      setUser(user)
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  if (!status) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Spinner />
      </div>
    )
  }

  const isSignup = mode === 'signup'
  return (
    <div className="flex min-h-screen items-center justify-center px-4">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex items-center justify-center gap-2 text-xl font-semibold tracking-tight">
          <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-indigo-600 text-white">K</span>
          Keel
        </div>
        <Card className="p-6">
          <h1 className="text-lg font-semibold">
            {status.needs_setup ? 'Create the owner account' : isSignup ? 'Create an account' : 'Log in'}
          </h1>
          {status.needs_setup && (
            <p className="mt-1 text-sm text-slate-500">This is a fresh panel. The first account you create becomes its owner.</p>
          )}
          <form onSubmit={submit} className="mt-5 space-y-4">
            <Field label="Email" type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
            <Field
              label="Password"
              type="password"
              autoComplete={isSignup ? 'new-password' : 'current-password'}
              minLength={isSignup ? 8 : undefined}
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            <ErrorNote>{error}</ErrorNote>
            <Button type="submit" disabled={busy} className="w-full">
              {busy && <Spinner />}
              {isSignup ? 'Create account' : 'Log in'}
            </Button>
          </form>
        </Card>
        {status.signup_open && !status.needs_setup && (
          <p className="mt-4 text-center text-sm text-slate-500">
            {isSignup ? 'Already have an account? ' : 'New here? '}
            <button className="font-medium text-indigo-600 hover:underline" onClick={() => setMode(isSignup ? 'login' : 'signup')}>
              {isSignup ? 'Log in' : 'Create an account'}
            </button>
          </p>
        )}
      </div>
    </div>
  )
}
