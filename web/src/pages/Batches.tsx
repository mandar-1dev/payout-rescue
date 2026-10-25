import { useRef, useState, type ChangeEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, checkDuplicates, seedConflicts } from '../lib/api'
import type { BatchRow, DuplicateWarning, Mode, Session } from '../lib/types'
import { money, when } from '../lib/format'
import { Empty, ErrorNote, ModeBadge, Skeleton, useToast } from '../components/ui'

interface Row { name: string; email: string; amount: string; currency: string; sim_tag: string; fake_outcome: string }
const blank = (): Row => ({ name: '', email: '', amount: '25.00', currency: 'USD', sim_tag: 'ERRPYO059', fake_outcome: '' })
const sample = (): Row => ({ ...blank(), name: 'Jordan Rivera', email: 'jordan.rivera@example.com', amount: '40.00' })

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

/* ------------------------------------------------------------------ CSV */

/** A small RFC-4180 style parser: quoted fields, escaped quotes, commas and newlines inside quotes. */
export function parseCsv(text: string): string[][] {
  const rows: string[][] = []
  let row: string[] = []
  let cell = ''
  let quoted = false
  const src = text.replace(/^\uFEFF/, '')
  for (let i = 0; i < src.length; i++) {
    const ch = src[i]
    if (quoted) {
      if (ch === '"' && src[i + 1] === '"') { cell += '"'; i++ }
      else if (ch === '"') quoted = false
      else cell += ch
    } else if (ch === '"') quoted = true
    else if (ch === ',') { row.push(cell); cell = '' }
    else if (ch === '\n' || ch === '\r') {
      if (ch === '\r' && src[i + 1] === '\n') i++
      row.push(cell); cell = ''
      if (row.some((c) => c.trim() !== '')) rows.push(row)
      row = []
    } else cell += ch
  }
  row.push(cell)
  if (row.some((c) => c.trim() !== '')) rows.push(row)
  return rows
}

interface Parsed { line: number; name: string; email: string; amount: string; currency: string; problem: string }

const emailOk = (s: string) => /^[^\s@<>",;]+@[^\s@<>",;]+\.[^\s@<>",;]{2,}$/.test(s) && s.length <= 254
const amountOk = (s: string) => /^\d{1,9}(\.\d{1,2})?$/.test(s) && parseFloat(s) > 0

export function validateCsv(text: string): Parsed[] {
  const rows = parseCsv(text)
  if (rows.length === 0) return []
  const header = rows[0].map((c) => c.trim().toLowerCase())
  const hasHeader = header.includes('email')
  const col = (name: string, fallback: number) => (hasHeader ? header.indexOf(name) : fallback)
  const idx = { name: col('name', 0), email: col('email', 1), amount: col('amount', 2), currency: col('currency', 3) }
  const seen = new Map<string, number>()
  return rows.slice(hasHeader ? 1 : 0).map((r, i) => {
    const line = i + 1 + (hasHeader ? 1 : 0)
    const get = (k: number) => (k >= 0 && k < r.length ? r[k].trim() : '')
    const p: Parsed = { line, name: get(idx.name), email: get(idx.email), amount: get(idx.amount).replace(/^\$/, ''), currency: (get(idx.currency) || 'USD').toUpperCase(), problem: '' }
    if (!p.name || p.name.length > 120) p.problem = 'Name is missing or longer than 120 characters'
    else if (!emailOk(p.email)) p.problem = 'Email is not valid'
    else if (!amountOk(p.amount)) p.problem = 'Amount must be a positive number with up to 2 decimals'
    else if (!/^[A-Z]{3}$/.test(p.currency)) p.problem = 'Currency must be a 3-letter code'
    else {
      const key = `${p.email.toLowerCase()}|${p.amount}|${p.currency}`
      const first = seen.get(key)
      if (first) p.problem = `Repeats line ${first} in this file`
      else seen.set(key, line)
    }
    return p
  })
}

/* ------------------------------------------------------------------ page */

export function Batches() {
  const nav = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()
  const fileRef = useRef<HTMLInputElement>(null)
  const session = useQuery({ queryKey: ['session'], queryFn: () => api<Session>('/api/session') })
  const batches = useQuery({ queryKey: ['batches'], queryFn: () => api<BatchRow[]>('/api/batches') })
  const [mode, setMode] = useState<Mode>('FAKE')
  const [rows, setRows] = useState<Row[]>([sample()])
  const [imported, setImported] = useState<{ file: string; parsed: Parsed[] } | null>(null)
  const [dups, setDups] = useState<DuplicateWarning[]>([])

  const create = useMutation({
    mutationFn: () => api<{ message?: string }>('/api/batches', {
      method: 'POST',
      json: { mode, recipients: rows.map((r) => ({ name: r.name, email: r.email, amount: r.amount, currency: r.currency, sim_tag: mode === 'SIMULATED' ? r.sim_tag : '', fake_outcome: mode === 'FAKE' ? r.fake_outcome : '' })) },
    }),
    onSuccess: (r) => { toast(r?.message ?? 'Batch sent.', r?.message ? 'warn' : 'ok'); void qc.invalidateQueries(); nav('/') },
    onError: (e: Error) => toast(e.message, 'error'),
  })
  const checking = useMutation({
    mutationFn: () => checkDuplicates(rows.map((r) => ({ email: r.email, amount: r.amount, currency: r.currency }))),
    onSuccess: (r) => { if (r.duplicates.length === 0) create.mutate(); else setDups(r.duplicates) },
    onError: (e: Error) => toast(e.message, 'error'),
  })
  const seed = useMutation({
    mutationFn: () => api<{ created: string[] }>('/api/demo/seed', { method: 'POST' }),
    onSuccess: (r) => { toast(`Created: ${r.created.join('; ')}`); void qc.invalidateQueries(); nav('/') },
    onError: (e: Error) => toast(e.message, 'error'),
  })
  const seedConf = useMutation({
    mutationFn: () => seedConflicts(),
    onSuccess: () => { toast('Conflict pack loaded. Open Conflicts to resolve them.'); void qc.invalidateQueries(); nav('/conflicts') },
    onError: (e: Error) => toast(e.message, 'error'),
  })

  const s = session.data
  const set = (i: number, patch: Partial<Row>) => { setDups([]); setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r))) }
  const modes: { id: Mode; label: string; ok: boolean; hint: string }[] = [
    { id: 'FAKE', label: 'Fake', ok: !!s?.fake_enabled, hint: 'In-memory PayPal stand-in. Can show held, blocked and day-30 behaviour.' },
    { id: 'SIMULATED', label: 'Simulated', ok: !!s?.live_configured, hint: 'PayPal’s documented test values return canned responses from the sandbox.' },
    { id: 'LIVE_SANDBOX', label: 'Live sandbox', ok: !!s?.live_configured, hint: 'Real sandbox payouts. Use emails with no PayPal account to get UNCLAIMED.' },
  ]

  function onFile(e: ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0]
    e.target.value = ''
    if (!f) return
    if (f.size > 1_000_000) { toast('That file is too large (max 1 MB).', 'error'); return }
    void f.text().then((t) => {
      const parsed = validateCsv(t)
      if (parsed.length === 0) toast('The file has no rows.', 'warn')
      else if (parsed.length > 100) toast('A batch holds at most 100 recipients. Split the file.', 'warn')
      else setImported({ file: f.name, parsed })
    })
  }
  function useValidRows() {
    if (!imported) return
    const good: Row[] = imported.parsed.filter((p) => !p.problem).map((p) => ({ ...blank(), name: p.name, email: p.email, amount: p.amount, currency: p.currency }))
    const untouched = rows.length === 1 && rows[0].name === sample().name && rows[0].email === sample().email
    setRows(untouched ? good : [...rows, ...good].slice(0, 100))
    setDups([])
    toast(`${good.length} recipients added to the form.`)
    setImported(null)
  }

  const bad = imported?.parsed.filter((p) => p.problem).length ?? 0
  const ready = rows.every((r) => r.name.trim() && r.email.trim() && r.amount.trim())

  return (
    <div className="grid gap-5 lg:grid-cols-[1.4fr_1fr]">
      <section className="panel space-y-4 p-5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-lg font-bold">Create a payout batch</h2>
          <div className="flex flex-wrap gap-2">
            <button className="btn btn-quiet" disabled={seed.isPending} onClick={() => seed.mutate()}>Load demo data</button>
            <button className="btn btn-quiet" disabled={seedConf.isPending} onClick={() => seedConf.mutate()}>Load conflict pack</button>
          </div>
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

        <div className="flex flex-wrap items-center gap-2 rounded-md border border-dashed border-line p-3">
          <input ref={fileRef} type="file" accept=".csv,text/csv" className="sr-only" aria-label="Import recipients from a CSV file" onChange={onFile} />
          <button className="btn btn-quiet" onClick={() => fileRef.current?.click()}>Import CSV</button>
          <span className="text-[12px] text-muted">Columns: name, email, amount, currency (a header row is optional). You see a preview before anything is added.</span>
        </div>

        {imported && (
          <div className="rise space-y-2 rounded-md border border-marine/40 bg-marine/5 p-3" role="region" aria-label="CSV preview">
            <p className="font-semibold">{imported.file}: {imported.parsed.length - bad} valid, {bad} with problems</p>
            <div className="max-h-64 overflow-auto rounded-md border border-line bg-surface">
              <table className="w-full min-w-[520px] text-left text-[13px]">
                <thead className="sticky top-0 bg-sunken"><tr className="text-[12px] text-muted"><th className="px-2 py-1.5 font-semibold">Line</th><th className="px-2 py-1.5 font-semibold">Name</th><th className="px-2 py-1.5 font-semibold">Email</th><th className="px-2 py-1.5 font-semibold">Amount</th><th className="px-2 py-1.5 font-semibold">Result</th></tr></thead>
                <tbody>
                  {imported.parsed.map((p) => (
                    <tr key={p.line} className="border-t border-line">
                      <td className="num px-2 py-1.5 text-muted">{p.line}</td><td className="px-2 py-1.5">{p.name}</td><td className="px-2 py-1.5">{p.email}</td>
                      <td className="num px-2 py-1.5">{p.amount ? money(p.amount, /^[A-Z]{3}$/.test(p.currency) ? p.currency : 'USD') : ''}</td>
                      <td className="px-2 py-1.5">{p.problem ? <span className="font-semibold text-brick">{p.problem}</span> : <span className="font-semibold text-pine">OK</span>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="flex flex-wrap gap-2">
              <button className="btn btn-primary" disabled={imported.parsed.length - bad === 0} onClick={useValidRows}>Use {imported.parsed.length - bad} valid rows</button>
              <button className="btn btn-quiet" onClick={() => setImported(null)}>Discard</button>
              {bad > 0 && <span className="self-center text-[12px] text-muted">Rows with problems are never imported.</span>}
            </div>
          </div>
        )}

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
              {rows.length > 1 && <button className="btn btn-quiet col-span-full justify-self-start" onClick={() => { setDups([]); setRows((rs) => rs.filter((_, j) => j !== i)) }}>Remove recipient</button>}
            </div>
          ))}
          <button className="btn btn-quiet" onClick={() => setRows((rs) => [...rs, blank()])} disabled={rows.length >= 100}>Add recipient</button>
          <span className="ml-2 text-[12px] text-muted">{rows.length} of 100</span>
        </div>

        {dups.length > 0 && (
          <div role="alert" className="rise space-y-2 rounded-md border border-signal/50 bg-signal/10 p-3">
            <p className="font-bold text-signal">This batch repeats a recent payout</p>
            <ul className="list-disc pl-5">
              {dups.map((d) => <li key={d.index}>Recipient {d.index + 1} ({d.email}) already had {money(d.amount, d.currency)} in the last 7 days ({when(d.created_at)}).</li>)}
            </ul>
            <p className="text-[13px] text-muted">If this is a second, real payment, confirm. If not, remove those recipients.</p>
            <div className="flex gap-2">
              <button className="btn btn-primary" disabled={create.isPending} onClick={() => create.mutate()}>{create.isPending ? 'Sending…' : 'Yes, send anyway'}</button>
              <button className="btn btn-quiet" onClick={() => setDups([])}>Go back</button>
            </div>
          </div>
        )}

        <div className="flex items-center gap-3">
          <button className="btn btn-primary" disabled={create.isPending || checking.isPending || !ready} onClick={() => checking.mutate()}>
            {create.isPending || checking.isPending ? 'Sending…' : 'Send batch'}
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
