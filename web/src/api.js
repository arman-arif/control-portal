export class ApiError extends Error {
  constructor(status, message) {
    super(message)
    this.status = status
  }
}

async function request(method, path, body) {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      // Required by the panel on every state-changing call (CSRF guard).
      'X-Keel-Request': '1',
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 204) return null
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    // The session expired or was revoked: send the user back to the login screen.
    if (res.status === 401 && !path.startsWith('/api/auth/')) {
      window.dispatchEvent(new Event('keel:logged-out'))
    }
    throw new ApiError(res.status, data?.error || `Request failed (${res.status})`)
  }
  return data
}

export const api = {
  authStatus: () => request('GET', '/api/auth/status'),
  me: () => request('GET', '/api/auth/me'),
  signup: (email, password) => request('POST', '/api/auth/signup', { email, password }),
  login: (email, password) => request('POST', '/api/auth/login', { email, password }),
  logout: () => request('POST', '/api/auth/logout'),

  servers: () => request('GET', '/api/servers'),
  server: (id) => request('GET', `/api/servers/${id}`),
  createServer: (name) => request('POST', '/api/servers', { name }),
  deleteServer: (id) => request('DELETE', `/api/servers/${id}`),
  installCommand: (id) => request('POST', `/api/servers/${id}/install-command`),
  stats: (id) => request('GET', `/api/servers/${id}/stats`),
  serviceAction: (id, name, action) =>
    request('POST', `/api/servers/${id}/services/${encodeURIComponent(name)}/${action}`),
  refreshPackages: (id) => request('POST', `/api/servers/${id}/packages/refresh`),
  stackInstall: (id, component, version) =>
    request('POST', `/api/servers/${id}/stack/install`, version ? { component, version } : { component }),
  jobs: (id) => request('GET', `/api/servers/${id}/jobs`),
  sites: (serverId) => request('GET', `/api/servers/${serverId}/sites`),
  createSite: (serverId, site) => request('POST', `/api/servers/${serverId}/sites`, site),
  site: (id) => request('GET', `/api/sites/${id}`),
  updateSite: (id, changes) => request('PATCH', `/api/sites/${id}`, changes),
  databases: (serverId) => request('GET', `/api/servers/${serverId}/databases`),
  createDatabase: (serverId, name, createUser) => request('POST', `/api/servers/${serverId}/databases`, { name, create_user: createUser }),
  deleteDatabase: (id) => request('DELETE', `/api/databases/${id}`),
  createDBUser: (serverId, name, databaseIds) =>
    request('POST', `/api/servers/${serverId}/database-users`, { name, database_ids: databaseIds }),
  setDBUserDatabases: (id, databaseIds) => request('PUT', `/api/database-users/${id}/databases`, { database_ids: databaseIds }),
  dbUserPassword: (id) => request('GET', `/api/database-users/${id}/password`),
  resetDBUserPassword: (id) => request('POST', `/api/database-users/${id}/password`),
  deleteDBUser: (id) => request('DELETE', `/api/database-users/${id}`),
  issueSSL: (id, email) => request('POST', `/api/sites/${id}/ssl`, { email }),
  deleteSite: (id, deleteFiles) => request('DELETE', `/api/sites/${id}${deleteFiles ? '?delete_files=true' : ''}`),
  job: (id, after = 0) => request('GET', `/api/jobs/${id}?after=${after}`),
}
