// Small hand-built UI pieces shared by the pages.
import { useState } from 'react'

const buttonStyles = {
  primary: 'bg-indigo-600 text-white hover:bg-indigo-500 disabled:bg-indigo-300',
  secondary: 'bg-white text-slate-700 ring-1 ring-inset ring-slate-300 hover:bg-slate-50 disabled:text-slate-400',
  danger: 'bg-white text-red-600 ring-1 ring-inset ring-red-200 hover:bg-red-50 disabled:text-red-300',
}

export function Button({ variant = 'primary', className = '', ...props }) {
  return (
    <button
      className={`inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium shadow-sm transition-colors disabled:cursor-not-allowed ${buttonStyles[variant]} ${className}`}
      {...props}
    />
  )
}

export function Card({ className = '', children }) {
  return <div className={`rounded-lg border border-slate-200 bg-white shadow-sm ${className}`}>{children}</div>
}

export function Field({ label, ...props }) {
  return (
    <label className="block">
      <span className="mb-1 block text-sm font-medium text-slate-700">{label}</span>
      <input
        className="block w-full rounded-md border border-slate-300 px-3 py-2 text-sm shadow-sm outline-none focus:border-indigo-500 focus:ring-2 focus:ring-indigo-200"
        {...props}
      />
    </label>
  )
}

export function ErrorNote({ children }) {
  if (!children) return null
  return <p className="rounded-md bg-red-50 px-3 py-2 text-sm text-red-700">{children}</p>
}

export function StatusDot({ online }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-sm">
      <span className={`h-2 w-2 rounded-full ${online ? 'bg-emerald-500' : 'bg-slate-300'}`} />
      <span className={online ? 'text-emerald-700' : 'text-slate-500'}>{online ? 'Online' : 'Offline'}</span>
    </span>
  )
}

export function CopyBox({ text }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard can be blocked on plain http; the text is still selectable.
    }
  }
  return (
    <div className="flex items-stretch overflow-hidden rounded-md border border-slate-800 bg-slate-900">
      <code className="flex-1 overflow-x-auto px-3 py-2.5 font-mono text-xs whitespace-nowrap text-slate-100 select-all">
        {text}
      </code>
      <button onClick={copy} className="border-l border-slate-700 px-3 text-xs font-medium text-slate-200 hover:bg-slate-800">
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  )
}

export function Meter({ value }) {
  const v = Math.min(100, Math.max(0, value || 0))
  const color = v > 90 ? 'bg-red-500' : v > 75 ? 'bg-amber-500' : 'bg-indigo-500'
  return (
    <div className="h-1.5 w-full overflow-hidden rounded-full bg-slate-100">
      <div className={`h-full rounded-full ${color}`} style={{ width: `${v}%` }} />
    </div>
  )
}

// Sparkline draws a 0-100 series as a small area chart.
export function Sparkline({ values, className = '' }) {
  const w = 300
  const h = 60
  if (values.length < 2) {
    return <div className={`flex h-16 items-center text-xs text-slate-400 ${className}`}>Collecting data…</div>
  }
  const step = w / (values.length - 1)
  const pts = values.map((v, i) => [i * step, h - (Math.min(100, Math.max(0, v)) / 100) * h])
  const line = pts.map(([x, y], i) => `${i ? 'L' : 'M'}${x.toFixed(1)},${y.toFixed(1)}`).join(' ')
  return (
    <svg viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" className={`h-16 w-full ${className}`}>
      <path d={`${line} L${w},${h} L0,${h} Z`} className="fill-indigo-100" />
      <path d={line} className="fill-none stroke-indigo-500" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
    </svg>
  )
}

export function Spinner() {
  return <span className="inline-block h-4 w-4 animate-spin rounded-full border-2 border-slate-300 border-t-indigo-600" />
}
