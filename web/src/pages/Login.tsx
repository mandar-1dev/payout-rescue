import { useState, type FormEvent } from 'react'
import { api, setToken, ApiError } from '../lib/api'
import { Mark } from '../components/Brand'

export function Login({ platform, onDone }: { platform: string; onDone: () => void }) {
  const [token, setTok] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    setToken(token.trim())
    try {
      await api('/api/session')
      onDone()
    } catch (err) {
      setError(err instanceof ApiError && err.status === 401 ? 'That token was not accepted.' : 'Could not reach the server.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="grid min-h-full place-items-center p-6">
      <form onSubmit={submit} className="panel rise w-full max-w-sm space-y-4 p-6 shadow-lift">
        <div className="flex items-center gap-3">
          <Mark size={36} />
          <div>
            <h1 className="text-lg font-bold leading-tight">PayoutRescue</h1>
            <p className="text-muted">{platform} payouts desk</p>
          </div>
        </div>
        <p className="text-muted">
          Enter the admin token to open the desk. Everything here runs against the PayPal sandbox; no real money moves.
        </p>
        <label className="block">
          <span className="mb-1 block font-semibold">Admin token</span>
          <input className="field" type="password" autoComplete="current-password" value={token} onChange={(e) => setTok(e.target.value)} required autoFocus />
        </label>
        {error && <p role="alert" className="text-brick">{error}</p>}
        <button className="btn btn-primary w-full" disabled={busy || !token.trim()}>
          {busy ? 'Checking…' : 'Open the desk'}
        </button>
      </form>
    </main>
  )
}
