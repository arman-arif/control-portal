import { CopyBox } from './ui.jsx'

export default function InstallSteps({ install }) {
  const expires = new Date(install.expires_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return (
    <div className="space-y-3">
      <h2 className="font-medium">Run this on your server as root</h2>
      <p className="text-sm text-slate-500">
        SSH into the server and paste the command. It installs the Keel agent and links the server to this panel. The
        command works once and expires at {expires}.
      </p>
      <CopyBox text={install.command} />
    </div>
  )
}
