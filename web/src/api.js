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
}
