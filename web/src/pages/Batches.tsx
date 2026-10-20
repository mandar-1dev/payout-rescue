import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import type { BatchRow, Mode, Session } from '../lib/types'
import { when } from '../lib/format'
import { Empty, ErrorNote, ModeBadge, Skeleton, useToast } from '../components/ui'

interface Row { name: string; email: string; amount: string; currency: string; sim_tag: string; fake_outcome: string }
const blank = (): Row => ({ name: '', email: '', amount: '25.00', currency: 'USD', sim_tag: 'ERRPYO059', fake_outcome: '' })

const fakeOutcomes: [string, string][] = [
  ['', 'Automatic (registered email pays, unknown email is unclaimed)'],
  ['ONHOLD', 'Held by PayPal'],
  ['BLOCKED', 'Blocked by PayPal'],
  ['FAILED:INVALID_EMAIL', 'Fails: invalid email'],
  ['FAILED:RECEIVER_ACCOUNT_LOCKED', 'Fails: account locked'],
  ['FAILED:CURRENCY_NOT_SUPPORTED_FOR_RECEIVER', 'Fails: currency not supported'],
  ['FAILED:RECEIVER_COUNTRY_NOT_ALLOWED', 'Fails: country not allowed'],
  ['FAILED:RECEIVING_LIMIT_EXCEEDED', 'Fails: receiving limit'],
]

export function Batches() {
  const nav = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()
  const session = useQuery({ queryKey: ['session'], queryFn: () => api<Session>('/api/session') })
  const batches = useQuery({ queryKey: ['batches'], queryFn: () => api<BatchRow[]>('/api/batches') })
  const [mode, setMode] = useState<Mode>('FAKE')
  const [rows, setRows] = useState<Row[]>([{ ...blank(), name: 'Jordan Rivera', email: 'jordan.rivera@example.com', amount: '40.00' }])

  const create = useMutation({
    mutationFn: () => api<{ message?: string }>('/api/batches', {
      method: 'POST',
      json: { mode, recipients: rows.map((r) => ({ name: r.name, email: r.email, amount: r.amount, currency: r.currency, sim_tag: mode === 'SIMULATED' ? r.sim_tag : '', fake_outcome: mode === 'FAKE' ? r.fake_outcome : '' })) },
    }),
    onSuccess: (r) => { toast(r?.message ?? 'Batch sent.', r?.message ? 'warn' : 'ok'); void qc.invalidateQueries(); nav('/') },
    onError: (e: Error) => toast(e.message, 'error'),
  })
  const seed = useMutation({
    mutationFn: () => api<{ created: string[] }>('/api/demo/seed', { method: 'POST' }),
    onSuccess: (r) => { toast(`Created: ${r.created.join('; ')}`); void qc.invalidateQueries(); nav('/') },
    onError: (e: Error) => toast(e.message, 'error'),
  })

  const s = session.data
  const set = (i: number, patch: Partial<Row>) => setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  const modes: { id: Mode; label: string; ok: boolean; hint: string }[] = [
    { id: 'FAKE', label: 'Fake', ok: !!s?.fake_enabled, hint: 'In-memory PayPal stand-in. Can show held, blocked and day-30 behaviour.' },
    { id: 'SIMULATED', label: 'Simulated', ok: !!s?.live_configured, hint: 'PayPal’s documented test values return canned responses from the sandbox.' },
    { id: 'LIVE_SANDBOX', label: 'Live sandbox', ok: !!s?.live_configured, hint: 'Real sandbox payouts. Use emails with no PayPal account to get UNCLAIMED.' },
  ]

  return (
    <div className="grid gap-5 lg:grid-cols-[1.4fr_1fr]">
      <section className="panel space-y-4 p-5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-lg font-bold">Create a payout batch</h2>
          <button className="btn btn-quiet" disabled={seed.isPending} onClick={() => seed.mutate()}>Load demo data</button>
        </div>

        <fieldset>
          <legend className="h-section mb-1.5">How should this batch run?</legend>
          <div className="grid gap-2 sm:grid-cols-3">
            {modes.map((m) => (
              <label key={m.id} className={`cursor-pointer rounded-md border p-2.5 ${mode === m.id ? 'border-marine bg-marine/5' : 'border-line'} ${m.ok ? '' : 'opacity-50'}`}>
                <input type="radio" name="mode" className="sr-only" disabled={!m.ok} checked={mode === m.id} onChange={() => setMode(m.id)} />
                <span className="font-semibold">{m.label}{!m.ok && ' (needs sandbox credentials)'}</span>
                <span className="mt-0.5 block text-[12px] text-muted">{m.hint}</span>
              </label>
            ))}
          </div>
        </fieldset>

        <div className="space-y-2">
          {rows.map((r, i) => (
            <div key={i} className="grid grid-cols-2 gap-2 rounded-md border border-line p-2.5 md:grid-cols-[1.1fr_1.5fr_90px_72px]">
              <input aria-label={`Name ${i + 1}`} className="field" placeholder="Name" value={r.name} onChange={(e) => set(i, { name: e.target.value })} />
              <input aria-label={`Email ${i + 1}`} className="field" placeholder="Email" value={r.email} onChange={(e) => set(i, { email: e.target.value })} />
              <input aria-label={`Amount ${i + 1}`} className="field num" inputMode="decimal" placeholder="Amount" value={r.amount} onChange={(e) => set(i, { amount: e.target.value })} />
              <input aria-label={`Currency ${i + 1}`} className="field" maxLength={3} value={r.currency} onChange={(e) => set(i, { currency: e.target.value.toUpperCase() })} />
              {mode === 'SIMULATED' && (
                <select aria-label={`Test value ${i + 1}`} className="field col-span-full" value={r.sim_tag} onChange={(e) => set(i, { sim_tag: e.target.value })}>
                  {Object.entries(s?.sim_tags ?? {}).map(([tag, what]) => <option key={tag} value={tag}>{tag} simulates {what}</option>)}
                </select>
              )}
              {mode === 'FAKE' && (
                <select aria-label={`Scripted outcome ${i + 1}`} className="field col-span-full" value={r.fake_outcome} onChange={(e) => set(i, { fake_outcome: e.target.value })}>
                  {fakeOutcomes.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
                </select>
              )}
              {rows.length > 1 && <button className="btn btn-quiet col-span-full justify-self-start" onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}>Remove recipient</button>}
            </div>
          ))}
          <button className="btn btn-quiet" onClick={() => setRows((rs) => [...rs, blank()])} disabled={rows.length >= 100}>Add recipient</button>
        </div>

        <div className="flex items-center gap-3">
          <button className="btn btn-primary" disabled={create.isPending || rows.some((r) => !r.name.trim() || !r.email.trim() || !r.amount.trim())} onClick={() => create.mutate()}>
            {create.isPending ? 'Sending…' : 'Send batch'}
          </button>
          <span className="text-[12px] text-muted">Sandbox only. No real money moves.</span>
        </div>
      </section>

      <section className="space-y-3">
        <h2 className="h-section">Recent batches</h2>
        {batches.isLoading ? <Skeleton className="h-32" /> : batches.error ? <ErrorNote error={batches.error} /> : (batches.data?.length ?? 0) === 0 ? <Empty title="No batches yet" /> : (
          <ul className="space-y-2">
            {batches.data!.map((b) => (
              <li key={b.id} className="panel flex flex-wrap items-center gap-2 p-3">
                <ModeBadge mode={b.mode} />
                <span className="font-mono text-[12px]">{b.sender_batch_id}</span>
                <span className="ml-auto text-muted">{when(b.created_at)}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
