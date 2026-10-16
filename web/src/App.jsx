import { createContext, useContext, useEffect, useState } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { api } from './api.js'
import Layout from './components/Layout.jsx'
import { Spinner } from './components/ui.jsx'
import AuthPage from './pages/AuthPage.jsx'
import Servers from './pages/Servers.jsx'
import AddServer from './pages/AddServer.jsx'
import ServerDetail from './pages/ServerDetail.jsx'

const AuthContext = createContext(null)
export const useAuth = () => useContext(AuthContext)

export default function App() {
  const [user, setUser] = useState(undefined) // undefined while loading, null when logged out

  useEffect(() => {
    api.me().then(setUser).catch(() => setUser(null))
    const onLoggedOut = () => setUser(null)
    window.addEventListener('keel:logged-out', onLoggedOut)
    return () => window.removeEventListener('keel:logged-out', onLoggedOut)
  }, [])

  if (user === undefined) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Spinner />
      </div>
    )
  }

  return (
    <AuthContext.Provider value={{ user, setUser }}>
      <Routes>
        {user ? (
          <Route element={<Layout />}>
            <Route path="/" element={<Navigate to="/servers" replace />} />
            <Route path="/servers" element={<Servers />} />
            <Route path="/servers/new" element={<AddServer />} />
            <Route path="/servers/:id" element={<ServerDetail />} />
            <Route path="*" element={<Navigate to="/servers" replace />} />
          </Route>
        ) : (
          <Route path="*" element={<AuthPage />} />
        )}
      </Routes>
    </AuthContext.Provider>
  )
}
