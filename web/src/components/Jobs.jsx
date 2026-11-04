// Queued jobs on a server: the recent list and a live log of one job.
import { useEffect, useRef, useState } from 'react'
import { api } from '../api.js'
import { usePolling } from '../usePolling.js'
import { Button, Card, ErrorNote, Spinner } from './ui.jsx'
import { timeAgo } from '../format.js'

const actionLabels = {
  'packages.refresh': 'Refresh package lists',
}

export const jobLabel = (action) => actionLabels[action] || action

const statusStyles = {
  queued: 'bg-slate-100 text-slate-600',
  running: 'bg-indigo-50 text-indigo-700',
  succeeded: 'bg-emerald-50 text-emerald-700',
  failed: 'bg-red-50 text-red-700',
}

export function JobStatus({ status }) {
  return <span className={`rounded-full px-2 py-0.5 text-xs font-medium ${statusStyles[status] || ''}`}>{status}</span>
}

const isActive = (status) => status === 'queued' || status === 'running'

// JobLog follows one job, fetching new output every second until it has
// finished and all output has arrived.
export function JobLog({ jobId }) {
  const [job, setJob] = useState(null)
  const [text, setText] = useState('')
  const [error, setError] = useState('')
  const pre = useRef(null)

  useEffect(() => {
    let after = 0
    let stopped = false
    let timer
    setJob(null)
    setText('')
    setError('')
    const poll = async () => {
      let more = false
      try {
        const res = await api.job(jobId, after)
        if (stopped) return
        setJob(res)
        if (res.logs.length) {
          after = res.logs[res.logs.length - 1].id
          setText((t) => t + res.logs.map((l) => l.text).join(''))
          more = true
        }
        if (!isActive(res.status) && !more) return
      } catch (err) {
        if (stopped) return
        setError(err.message)
      }
      timer = setTimeout(poll, more ? 0 : 1000)
    }
    poll()
    return () => {
      stopped = true
      clearTimeout(timer)
    }
  }, [jobId])

  // Keep the newest output in view unless the user scrolled up.
  useEffect(() => {
    const el = pre.current
    if (el && el.scrollHeight - el.scrollTop - el.clientHeight < 80) el.scrollTop = el.scrollHeight
  }, [text])

  return (
    <div className="space-y-2">
      {job && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <JobStatus status={job.status} />
          <span className="font-medium">{jobLabel(job.action)}</span>
          {job.status === 'running' && <Spinner />}
          {job.error && <span className="text-red-600">{job.error}</span>}
        </div>
      )}
      <pre
        ref={pre}
        className="max-h-96 overflow-auto rounded-md bg-slate-900 px-3 py-2.5 font-mono text-xs whitespace-pre-wrap text-slate-100"
      >
        {text || (job?.status === 'queued' ? 'Waiting for the server…' : job ? 'No output.' : 'Loading…')}
      </pre>
      <ErrorNote>{error}</ErrorNote>
    </div>
  )
}

export function JobsCard({ serverId, online }) {
  const { data: jobs, refresh } = usePolling(() => api.jobs(serverId), 3000)
  const [selected, setSelected] = useState(null)
  const [error, setError] = useState('')

  const refreshPackages = async () => {
    setError('')
    try {
      const job = await api.refreshPackages(serverId)
      setSelected(job.id)
      refresh()
    } catch (err) {
      setError(err.message)
    }
  }

  return (
    <Card>
      <div className="flex flex-wrap items-center gap-3 border-b border-slate-100 px-5 py-3">
        <h2 className="font-medium">Jobs</h2>
        <Button variant="secondary" className="ml-auto px-2.5 py-1 text-xs" onClick={refreshPackages}>
          Refresh package lists
        </Button>
      </div>
      {error && (
        <div className="px-5 pt-3">
          <ErrorNote>{error}</ErrorNote>
        </div>
      )}
      {!online && jobs?.some((j) => j.status === 'queued') && (
        <p className="px-5 pt-3 text-sm text-slate-500">Queued jobs start when the server comes back online.</p>
      )}
      {!jobs ? (
        <div className="px-5 py-6">
          <Spinner />
        </div>
      ) : jobs.length === 0 ? (
        <p className="px-5 py-6 text-sm text-slate-500">No jobs yet.</p>
      ) : (
        <ul className="divide-y divide-slate-100">
          {jobs.map((j) => (
            <li key={j.id}>
              <button
                className={`flex w-full flex-wrap items-center gap-3 px-5 py-3 text-left hover:bg-slate-50 ${selected === j.id ? 'bg-slate-50' : ''}`}
                onClick={() => setSelected(selected === j.id ? null : j.id)}
              >
                <JobStatus status={j.status} />
                <span className="text-sm">{jobLabel(j.action)}</span>
                <span className="ml-auto text-xs text-slate-500">{timeAgo(j.created_at)}</span>
              </button>
              {selected === j.id && (
                <div className="px-5 pb-4">
                  <JobLog jobId={j.id} />
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </Card>
  )
}
