import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { api } from '../api.js'
import { useAuth } from '../App.jsx'

export default function Layout() {
  const { user, setUser } = useAuth()
  const navigate = useNavigate()

  const logout = async () => {
    await api.logout().catch(() => {})
    setUser(null)
    navigate('/')
  }

  return (
    <div className="min-h-screen">
      <header className="border-b border-slate-200 bg-white">
        <div className="mx-auto flex h-14 max-w-6xl items-center gap-6 px-4">
          <NavLink to="/servers" className="flex items-center gap-2 font-semibold tracking-tight">
            <span className="flex h-7 w-7 items-center justify-center rounded-md bg-indigo-600 text-sm text-white">K</span>
            Keel
          </NavLink>
          <nav className="flex gap-1 text-sm">
            <NavLink
              to="/servers"
              className={({ isActive }) =>
                `rounded-md px-3 py-1.5 ${isActive ? 'bg-slate-100 font-medium text-slate-900' : 'text-slate-600 hover:text-slate-900'}`
              }
            >
              Servers
            </NavLink>
          </nav>
          <div className="ml-auto flex items-center gap-3 text-sm">
            <span className="hidden text-slate-500 sm:inline">{user.email}</span>
            <button onClick={logout} className="rounded-md px-2 py-1 text-slate-600 hover:bg-slate-100 hover:text-slate-900">
              Log out
            </button>
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-6xl px-4 py-8">
        <Outlet />
      </main>
    </div>
  )
}
