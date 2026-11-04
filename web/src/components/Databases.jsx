// MySQL/MariaDB databases and users on a server.
import { useState } from 'react'
import { api } from '../api.js'
import { usePolling } from '../usePolling.js'
import { Button, Card, CopyBox, ErrorNote, Field, Spinner } from './ui.jsx'
import { SiteStatus as Status } from './Sites.jsx'

const NAME_HINT = 'Lower-case letters, digits and underscores, starting with a letter.'

export function DatabasesCard({ serverId, online, inventory }) {
  const { data, error, refresh } = usePolling(() => api.databases(serverId), 3000)
  const [shown, setShown] = useState(null) // {user, password} to show once after creating or resetting

  const engine = inventory?.mysql ? `MySQL ${inventory.mysql}` : inventory?.mariadb ? `MariaDB ${inventory.mariadb}` : null
  if (inventory && !engine) {
    return <Card className="p-6 text-sm text-slate-500">Install MySQL or MariaDB from the Stack tab to create databases.</Card>
  }

  return (
    <div className="space-y-4">
      {engine && (
        <p className="text-sm text-slate-500">
          {engine}. Sites on this server connect to host <code className="font-mono">127.0.0.1</code>, port{' '}
          <code className="font-mono">3306</code>.
        </p>
      )}
      {error && <ErrorNote>{error.message}</ErrorNote>}
      {shown && (
        <Card className="space-y-2 border-emerald-200 p-5">
          <div className="flex items-center">
            <h2 className="font-medium">Password for {shown.user}</h2>
            <button className="ml-auto text-sm text-slate-500 hover:text-slate-900" onClick={() => setShown(null)}>
              Close
            </button>
          </div>
          <CopyBox text={shown.password} />
          <p className="text-xs text-slate-500">You can show it again later from the user's row.</p>
        </Card>
      )}
      <DatabaseList serverId={serverId} online={online} data={data} onChange={refresh} onPassword={setShown} />
      <UserList serverId={serverId} online={online} data={data} onChange={refresh} onPassword={setShown} />
    </div>
  )
}

function DatabaseList({ serverId, online, data, onChange, onPassword }) {
  const [name, setName] = useState('')
  const [withUser, setWithUser] = useState(true)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const create = async (e) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      const res = await api.createDatabase(serverId, name.trim(), withUser)
      if (res.password) onPassword({ user: res.user.name, password: res.password })
      if (res.user_error) setError(`The database was created, but not the user: ${res.user_error}`)
      setName('')
      onChange()
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  const remove = async (db) => {
    if (!confirm(`Drop database ${db.name}? Everything in it is deleted for good.`)) return
    try {
      await api.deleteDatabase(db.id)
      onChange()
    } catch (err) {
      setError(err.message)
    }
  }

  return (
    <Card>
      <div className="border-b border-slate-100 px-5 py-3">
        <h2 className="font-medium">Databases</h2>
      </div>
      <form onSubmit={create} className="flex flex-wrap items-end gap-3 border-b border-slate-100 px-5 py-4">
        <div className="w-64">
          <Field label="New database" placeholder="shop" value={name} onChange={(e) => setName(e.target.value)} required title={NAME_HINT} />
        </div>
        <label className="flex items-center gap-2 pb-2 text-sm">
          <input type="checkbox" checked={withUser} onChange={(e) => setWithUser(e.target.checked)} />
          Also create a user with the same name
        </label>
        <Button type="submit" disabled={busy || !online}>
          {busy && <Spinner />}
          Create
        </Button>
        <div className="w-full">
          <ErrorNote>{error}</ErrorNote>
        </div>
      </form>
      {!data ? (
        <div className="px-5 py-6">
          <Spinner />
        </div>
      ) : data.databases.length === 0 ? (
        <p className="px-5 py-6 text-sm text-slate-500">No databases yet.</p>
      ) : (
        <ul className="divide-y divide-slate-100">
          {data.databases.map((db) => (
            <li key={db.id} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-5 py-3">
              <span className="font-mono text-sm">{db.name}</span>
              <Status status={db.status} />
              <span className="text-sm text-slate-500">{db.users.length ? `Used by ${db.users.join(', ')}` : 'No users'}</span>
              {db.status === 'failed' && <span className="w-full text-xs text-red-600">{db.error}</span>}
              <Button
                variant="danger"
                className="ml-auto px-2.5 py-1 text-xs"
                disabled={!online || db.status === 'deleting'}
                onClick={() => remove(db)}
              >
                Drop
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  )
}

function UserList({ serverId, online, data, onChange, onPassword }) {
  const [name, setName] = useState('')
  const [picked, setPicked] = useState([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const databases = data?.databases ?? []

  const create = async (e) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      const res = await api.createDBUser(serverId, name.trim(), picked)
      onPassword({ user: res.user.name, password: res.password })
      setName('')
      setPicked([])
      onChange()
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <div className="border-b border-slate-100 px-5 py-3">
        <h2 className="font-medium">Users</h2>
      </div>
      <form onSubmit={create} className="space-y-3 border-b border-slate-100 px-5 py-4">
        <div className="w-64">
          <Field label="New user" placeholder="shop_reader" value={name} onChange={(e) => setName(e.target.value)} required title={NAME_HINT} />
        </div>
        <DatabasePicker databases={databases} picked={picked} onChange={setPicked} />
        <ErrorNote>{error}</ErrorNote>
        <Button type="submit" disabled={busy || !online}>
          {busy && <Spinner />}
          Create user
        </Button>
      </form>
      {!data ? null : data.users.length === 0 ? (
        <p className="px-5 py-6 text-sm text-slate-500">No users yet.</p>
      ) : (
        <ul className="divide-y divide-slate-100">
          {data.users.map((u) => (
            <UserRow key={u.id} user={u} databases={databases} online={online} onChange={onChange} onPassword={onPassword} />
          ))}
        </ul>
      )}
    </Card>
  )
}

function DatabasePicker({ databases, picked, onChange }) {
  if (databases.length === 0) return <p className="text-sm text-slate-500">Create a database to give the user access to it.</p>
  const toggle = (id) => onChange(picked.includes(id) ? picked.filter((x) => x !== id) : [...picked, id])
  return (
    <fieldset className="flex flex-wrap gap-x-4 gap-y-1">
      <legend className="mb-1 text-sm font-medium text-slate-700">Access to</legend>
      {databases.map((db) => (
        <label key={db.id} className="flex items-center gap-1.5 text-sm">
          <input type="checkbox" checked={picked.includes(db.id)} onChange={() => toggle(db.id)} />
          <span className="font-mono">{db.name}</span>
        </label>
      ))}
    </fieldset>
  )
}

function UserRow({ user, databases, online, onChange, onPassword }) {
  const [editing, setEditing] = useState(false)
  const [picked, setPicked] = useState(user.database_ids)
  const [error, setError] = useState('')
  const busy = user.status === 'deleting'

  const act = async (fn) => {
    setError('')
    try {
      await fn()
      onChange()
    } catch (err) {
      setError(err.message)
    }
  }
  const reveal = () => act(async () => onPassword({ user: user.name, password: (await api.dbUserPassword(user.id)).password }))
  const reset = () =>
    confirm(`Give ${user.name} a new password? Sites using the old one stop connecting until you update them.`) &&
    act(async () => onPassword({ user: user.name, password: (await api.resetDBUserPassword(user.id)).password }))
  const remove = () => confirm(`Delete database user ${user.name}?`) && act(() => api.deleteDBUser(user.id))
  const save = () =>
    act(async () => {
      await api.setDBUserDatabases(user.id, picked)
      setEditing(false)
    })

  return (
    <li className="space-y-2 px-5 py-3">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
        <span className="font-mono text-sm">{user.name}</span>
        <Status status={user.status} />
        <span className="text-sm text-slate-500">{user.databases.length ? user.databases.join(', ') : 'No databases'}</span>
        <div className="ml-auto flex flex-wrap gap-2">
          <Button variant="secondary" className="px-2.5 py-1 text-xs" onClick={reveal}>
            Show password
          </Button>
          <Button variant="secondary" className="px-2.5 py-1 text-xs" disabled={!online || busy} onClick={() => setEditing(!editing)}>
            Access
          </Button>
          <Button variant="secondary" className="px-2.5 py-1 text-xs" disabled={!online || busy} onClick={reset}>
            New password
          </Button>
          <Button variant="danger" className="px-2.5 py-1 text-xs" disabled={!online || busy} onClick={remove}>
            Delete
          </Button>
        </div>
      </div>
      {user.status === 'failed' && <p className="text-xs text-red-600">{user.error}</p>}
      {editing && (
        <div className="space-y-2 rounded-md bg-slate-50 p-3">
          <DatabasePicker databases={databases} picked={picked} onChange={setPicked} />
          <div className="flex gap-2">
            <Button className="px-2.5 py-1 text-xs" onClick={save}>
              Save access
            </Button>
            <Button variant="secondary" className="px-2.5 py-1 text-xs" onClick={() => setEditing(false)}>
              Cancel
            </Button>
          </div>
        </div>
      )}
      <ErrorNote>{error}</ErrorNote>
    </li>
  )
}
