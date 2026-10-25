import { Fragment, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, download, getDemoPayouts, getReference } from '../lib/api'
import type { DemoPayout, Item, RuleRow } from '../lib/types'
import { ErrorNote, ModeBadge, Skeleton, StatusChip, useToast } from '../components/ui'
import { explanations, money } from '../lib/format'
import { conflictInfo, resolutionLabels } from './Conflicts'

/* ------------------------------------------------------------------ static content */

interface Task { id: string; title: string; steps: string[]; link?: string; linkLabel?: string; csv?: boolean }

const tasks: Task[] = [
  { id: 'create-batch', title: 'Create a payout batch', link: '/batches', linkLabel: 'Open Batches', steps: ['Open Batches.', 'Pick how it runs: Fake (no PayPal needed), Simulated, or Live sandbox.', 'Fill in name, email, amount and currency for each recipient, or import a CSV.', 'Click Send batch. If it repeats a recent payout you are asked to confirm.'] },
  { id: 'import-csv', title: 'Import recipients from a CSV', link: '/batches', linkLabel: 'Open Batches', steps: ['Open Batches and choose Import CSV.', 'Use columns name, email, amount, currency (a header row is optional).', 'Read the preview. Rows with problems are listed with the reason and are never imported.', 'Click Use valid rows to fill the batch form, then Send batch.'] },
  { id: 'load-demo', title: 'Load the demo data', link: '/batches', linkLabel: 'Open Batches', steps: ['Open Batches.', 'Click Load demo data for 12 everyday situations.', 'Click Load conflict pack for 7 payouts that each end in a conflict.'] },
  { id: 'review', title: 'Review a payout', link: '/', linkLabel: 'Open Payouts', steps: ['Open Payouts and click any row, or its Review link.', 'Read the plain-words summary and, for unclaimed money, the 30-day rail.', 'Scroll for decisions waiting, conversation, notes, attempts and history.'] },
  { id: 'simulate-reply', title: 'Simulate a recipient reply', link: '/', linkLabel: 'Open Payouts', steps: ['Open a payout.', 'In Simulate the recipient’s reply, type a message or click a preset (two are attacks).', 'Click Send reply. The tags under the reply show what was found, or why it was blocked.'] },
  { id: 'corrected-email', title: 'Enter a corrected email yourself', link: '/', linkLabel: 'Open Payouts', steps: ['Open a failed or unclaimed payout.', 'Type the email under Or enter a corrected email yourself.', 'Click Record email. It is validated in code, then the system re-checks what to propose.'] },
  { id: 'approve-reject', title: 'Approve or reject a decision', link: '/approvals', linkLabel: 'Open Approvals', steps: ['Type your name in Acting as (left menu). It is recorded with every approval.', 'Open Approvals or the payout page.', 'For actions that move money, read the safety checks and the exact request.', 'Click Approve (PayPal is read again first) or Reject.'] },
  { id: 'cancel', title: 'Cancel an unclaimed payout', link: '/approvals', linkLabel: 'Open Approvals', steps: ['A cancel is proposed on day 27, or when a corrected email exists for an unclaimed payout.', 'Open the Cancel unclaimed payout card and approve it.', 'The system confirms with PayPal that it is cancelled before anything else happens.'] },
  { id: 'replace', title: 'Send one replacement', link: '/approvals', linkLabel: 'Open Approvals', steps: ['After the original is confirmed failed, returned or cancelled, and a corrected email exists, a replacement card appears.', 'Check every safety check has a green tick.', 'Approve. Exactly one new payout is sent; approving again is refused.'] },
  { id: 'snooze', title: 'Snooze a decision', link: '/approvals', linkLabel: 'Open Approvals', steps: ['On any waiting decision, open the Snooze menu.', 'Choose 1, 3 or 7 days.', 'It leaves the queue and returns afterwards. Nothing is approved by snoozing.'] },
  { id: 'bulk-ack', title: 'Acknowledge several messages at once', link: '/approvals', linkLabel: 'Open Approvals', steps: ['On Approvals, tick the boxes next to messages, notes and escalations.', 'Click Acknowledge selected.', 'Decisions that move money have no checkbox: they can never be approved in bulk.'] },
  { id: 'conflict', title: 'Resolve a conflict', link: '/conflicts', linkLabel: 'Open Conflicts', steps: ['Open Conflicts.', 'Read the explanation and the evidence.', 'Choose one of the allowed resolutions and add a note when asked.', 'Resolving never sends money by itself. The audit log records your name.'] },
  { id: 'notes', title: 'Add an operator note', link: '/', linkLabel: 'Open Payouts', steps: ['Open a payout and find Notes.', 'Type the note and click Add note.', 'It is saved with your name and appears in the history.'] },
  { id: 'fake-time', title: 'Move fake time forward', link: '/setup', linkLabel: 'Open Setup', steps: ['Open Setup.', 'Click +1, +7, +20, +27 or +30 days.', 'Fake unclaimed items age, so reminders, cancels and the day-30 return appear.'] },
  { id: 'reconcile', title: 'Reconcile now', link: '/setup', linkLabel: 'Open Setup', steps: ['Open Setup and click Reconcile now.', 'Every unfinished payout is read back from PayPal.', 'Missed webhooks and interrupted approvals are recovered.'] },
  { id: 'benchmark', title: 'Run the benchmark', link: '/benchmark', linkLabel: 'Open Benchmark', steps: ['Open Benchmark and click Run the benchmark.', 'Wait a few seconds.', 'Read the comparison. These are scripted scenarios, not real-world measurements.'] },
  { id: 'audit', title: 'Check and export the audit log', link: '/audit', linkLabel: 'Open Audit log', steps: ['Open Audit log. The tag says Chain intact when nothing was changed or removed.', 'Click Export CSV to download every entry.'], csv: true },
  { id: 'report', title: 'Print a one-page report for a payout', link: '/', linkLabel: 'Open Payouts', steps: ['Open a payout.', 'Click Print report.', 'The menu and buttons are hidden and a clean summary is printed or saved as PDF.'] },
  { id: 'palette', title: 'Jump anywhere with the command palette', steps: ['Press Ctrl+K (Cmd+K on a Mac).', 'Type a page, a payout name, or a command such as Reconcile now.', 'Use the arrow keys and press Enter.'] },
  { id: 'webhook', title: 'Register the PayPal webhook', link: '/setup', linkLabel: 'Open Setup', steps: ['Set PUBLIC_BASE_URL so the exact URL shows on Setup.', 'In the PayPal developer dashboard add it to your sandbox app and subscribe to payout item and batch events.', 'Copy the Webhook ID into PAYPAL_WEBHOOK_ID and restart.'] },
  { id: 'export', title: 'Export payouts as CSV', link: '/', linkLabel: 'Open Payouts', steps: ['Open Payouts.', 'Click Export CSV above the table.'] },
]

const statusGuide: [string, string][] = [
  ['CREATED', 'Saved here and being sent to PayPal.'], ['PENDING', 'PayPal is still processing it.'],
  ['UNCLAIMED', 'Waiting for the recipient to get a PayPal account. PayPal returns it after 30 days.'], ['SUCCESS', 'Paid.'],
  ['FAILED', 'PayPal refused it. The error name says why.'], ['ONHOLD', 'PayPal is holding it.'], ['BLOCKED', 'PayPal blocked it.'],
  ['RETURNED', 'Unclaimed money PayPal gave back after 30 days.'], ['CANCELED', 'Cancelled by you and returned.'],
]

const kindLabel: Record<string, string> = { notify: 'Message', cancel: 'Cancel', reissue: 'Replacement', escalate: 'Needs a decision', flag: 'Note' }

const invariants: [string, string][] = [
  ['Nothing moves money without a named human.', 'Cancelling and replacing are the only actions that move money, and each needs an approval with a name in the audit log.'],
  ['One live payout per person per obligation.', 'The database itself refuses a second active payout for the same obligation, even if the code were wrong.'],
  ['Replace only after PayPal confirms the original is dead.', 'The original must read back as failed, returned or cancelled at approval time. Webhooks and cached data are never trusted for this.'],
  ['Retries cannot double-send.', 'A retry reuses the same ids, so PayPal replays the answer instead of paying again.'],
  ['AI never decides.', 'It drafts text and picks an email out of a reply. It cannot approve anything, call PayPal, or choose an amount or currency.'],
  ['Sandbox only.', 'The server refuses to start with any PayPal address other than the sandbox.'],
  ['Everything is recorded and tamper-evident.', 'Each audit entry contains the hash of the one before it, and the database refuses edits and deletes.'],
  ['Resolving a conflict never moves money.', 'A resolution can record a validated email, withdraw a proposal, draft a message or close the case. Sending still needs its own approval.'],
]

const glossary: [string, string][] = [
  ['Logical payout', 'The obligation: pay this person this amount. It can have several attempts but never more than one live one.'],
  ['Item', 'One attempt to pay a logical payout through PayPal.'],
  ['In limbo', 'Money that has left your balance but has not reached the recipient: unclaimed, on hold, blocked or pending.'],
  ['Readback', 'Asking PayPal for the current state of an item.'],
  ['Idempotency key', 'A label sent with a request so repeating it cannot do the job twice.'],
  ['Reconciler', 'A background job that re-reads unfinished payouts to recover from missed webhooks.'],
  ['Conflict', 'A payout where the evidence disagrees, so a person must decide.'],
  ['Fake, Simulated, Live sandbox', 'Where the status came from: an in-memory stand-in, PayPal’s canned test responses, or real sandbox payouts.'],
]

const faq: [string, string][] = [
  ['Why can’t I click Approve?', 'Either your name is empty (see Acting as) or a safety check is failing. The card says which.'],
  ['Why was my approval “refused safely”?', 'PayPal was read again and the facts changed, for example the recipient claimed the money. Nothing was sent.'],
  ['Why is Live sandbox greyed out?', 'PAYPAL_CLIENT_ID and PAYPAL_CLIENT_SECRET are not set on the server.'],
  ['How do I test day 27 and day 30?', 'On Setup, move fake time forward. It only affects Fake items.'],
  ['Why can’t I tick a box next to a cancel or replacement?', 'Anything that moves money must be approved one at a time, on purpose.'],
  ['Does this move real money?', 'No. It only talks to the PayPal sandbox.'],
  ['Why are webhooks rejected?', 'PAYPAL_WEBHOOK_ID is missing or wrong. PayPal’s simulator events cannot be verified.'],
]

const shortcuts: [string, string][] = [
  ['Ctrl / ⌘ + K', 'Open the command palette'], ['?', 'Open help for the page you are on'],
  ['g then p', 'Go to Payouts'], ['g then a', 'Go to Approvals'], ['g then c', 'Go to Conflicts'], ['g then b', 'Go to Batches'],
  ['Esc', 'Close a dialog, the palette or the help drawer'], ['Tab / Shift+Tab', 'Move between controls; focus is always visible'],
]

const toc: [string, string][] = [
  ['start', '1. Start here'], ['tasks', '2. What you can do'], ['payouts', '3. Every demo payout'], ['dictionary', '4. Statuses and errors'],
  ['rules', '5. What the system proposes'], ['actions', '6. Actions and approvals'], ['conflicts', '7. Conflicts'], ['safety', '8. Safety guarantees'],
  ['screens', '9. Screens and buttons'], ['api', '10. API and webhook'], ['keys', '11. Keyboard'], ['glossary', '12. Glossary'], ['faq', '13. FAQ and troubleshooting'],
]

/* ------------------------------------------------------------------ pieces */

function Highlight({ text, q }: { text: string; q: string }) {
  if (!q) return <>{text}</>
  const lower = text.toLowerCase()
  const parts: ReactNode[] = []
  let at = 0
  for (let i = lower.indexOf(q, at); i !== -1; i = lower.indexOf(q, at)) {
    if (i > at) parts.push(text.slice(at, i))
    parts.push(<mark key={i} className="rounded bg-signal/30 px-0.5 text-ink">{text.slice(i, i + q.length)}</mark>)
    at = i + q.length
  }
  parts.push(text.slice(at))
  return <>{parts.map((p, i) => <Fragment key={i}>{p}</Fragment>)}</>
}

function Section({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return (
    <section id={id} className="scroll-mt-20 space-y-3 print:break-inside-avoid-page">
      <h2 className="border-b border-line pb-2 text-xl font-bold"><a href={`#${id}`} className="hover:underline">{title}</a></h2>
      {children}
    </section>
  )
}

function DemoTable({ rows, items, showKind }: { rows: DemoPayout[]; items: Map<string, Item>; showKind?: boolean }) {
  return (
    <div className="panel overflow-x-auto">
      <table className="w-full min-w-[860px] text-left">
        <thead>
          <tr className="text-[12px] text-muted">
            <th className="px-3 py-2 font-semibold">Name</th><th className="px-2 py-2 font-semibold">Amount</th>
            <th className="px-2 py-2 font-semibold">{showKind ? 'Conflict' : 'Source'}</th><th className="px-2 py-2 font-semibold">Now</th>
            <th className="px-2 py-2 font-semibold">Story and what you should see</th><th className="px-2 py-2 font-semibold">Try this</th><th className="px-3 py-2" />
          </tr>
        </thead>
        <tbody>
          {rows.map((d) => {
            const live = items.get(d.email.toLowerCase())
            return (
              <tr key={d.email} className="border-t border-line align-top">
                <td className="px-3 py-2"><p className="font-semibold">{d.name}</p><p className="text-[12px] text-muted">{d.email}</p></td>
                <td className="num whitespace-nowrap px-2 py-2">{money(d.amount, d.currency)}</td>
                <td className="px-2 py-2">{showKind ? <code className="text-[12px]">{d.conflict_kind}</code> : <ModeBadge mode={d.mode} />}</td>
                <td className="px-2 py-2">{live ? <StatusChip status={live.status} /> : <span className="text-[12px] text-muted">Not loaded</span>}</td>
                <td className="max-w-xs px-2 py-2"><p>{d.story}</p><p className="mt-1 font-semibold">{d.expected}</p></td>
                <td className="max-w-[240px] px-2 py-2 text-[12px] text-muted"><ul className="list-disc pl-4">{d.try_this.map((t) => <li key={t}>{t}</li>)}</ul></td>
                <td className="px-3 py-2">{live && <Link className="btn btn-quiet" to={`/items/${live.id}`}>Open</Link>}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function proposalText(r: RuleRow): { what: string; why: string } {
  const ps = r.proposals ?? []
  if (ps.length === 0) return { what: 'Nothing', why: 'No action is needed.' }
  return { what: ps.map((p) => kindLabel[p.Kind] ?? p.Kind).join(' + '), why: ps.map((p) => p.Reason).join(' ') }
}

/* ------------------------------------------------------------------ page */

export function Help() {
  const toast = useToast()
  const loc = useLocation()
  const [search, setSearch] = useState('')
  const reference = useQuery({ queryKey: ['reference'], queryFn: getReference })
  const demo = useQuery({ queryKey: ['demo-payouts'], queryFn: getDemoPayouts })
  const items = useQuery({ queryKey: ['items'], queryFn: () => api<Item[]>('/api/items') })

  useEffect(() => {
    if (loc.hash) document.getElementById(loc.hash.slice(1))?.scrollIntoView({ block: 'start' })
  }, [loc.hash, demo.isSuccess, reference.isSuccess])

  const byEmail = useMemo(() => {
    const m = new Map<string, Item>()
    for (const it of items.data ?? []) if (it.attempt === 1) m.set(it.recipient_email.toLowerCase(), it)
    return m
  }, [items.data])

  const q = search.trim().toLowerCase()
  const has = (...t: string[]) => !q || t.some((x) => x.toLowerCase().includes(q))
  const shownTasks = tasks.filter((t) => has(t.title, ...t.steps))
  const shownInvariants = invariants.filter(([a, b]) => has(a, b))
  const conflictKinds = Object.keys(conflictInfo).filter((k) => has(conflictInfo[k].title, conflictInfo[k].what, k))
  const shownFaq = faq.filter(([a, b]) => has(a, b))
  const shownGlossary = glossary.filter(([a, b]) => has(a, b))
  const standard = demo.data?.standard ?? []
  const conflicts = demo.data?.conflicts ?? []
  const total = standard.length + conflicts.length
  const loaded = [...standard, ...conflicts].filter((d) => byEmail.has(d.email.toLowerCase())).length
  const matrix = (reference.data?.rules_matrix ?? []).filter((r) => has(r.when, proposalText(r).what, proposalText(r).why))

  return (
    <div className="grid gap-8 lg:grid-cols-[220px_1fr]">
      <aside className="space-y-3 lg:sticky lg:top-20 lg:h-[calc(100vh-7rem)] lg:overflow-y-auto print:hidden">
        <input type="search" className="field" placeholder="Search the guide" aria-label="Search the guide" value={search} onChange={(e) => setSearch(e.target.value)} />
        <nav aria-label="Guide contents" className="space-y-0.5">
          {toc.map(([id, label]) => <a key={id} href={`#${id}`} className="block rounded px-2 py-1 text-muted hover:bg-sunken hover:text-ink">{label}</a>)}
        </nav>
        <div className="space-y-2">
          <button className="btn btn-quiet w-full" onClick={() => window.print()}>Print this guide</button>
          <button className="btn btn-quiet w-full" onClick={() => { localStorage.removeItem('payoutrescue.tour'); window.dispatchEvent(new Event('payoutrescue:tour')) }}>Take the tour</button>
        </div>
      </aside>

      <div className="min-w-0 max-w-5xl space-y-10">
        <Section id="start" title="1. Start here">
          <div className="panel space-y-3 p-5">
            <p className="text-[15px]">PayoutRescue watches the PayPal payouts you have sent. When one gets stuck, it explains why in plain words, suggests the safe next step, and waits for a named person to approve anything that moves money. It is built so nobody is ever paid twice.</p>
            <p className="rounded-md border border-signal/40 bg-signal/10 px-3 py-2 font-semibold text-signal">Sandbox only. Everything here uses PayPal’s practice environment. No real money moves.</p>
            <ol className="list-decimal space-y-1 pl-5 text-muted">
              <li><b className="text-ink">Payouts</b> lists every payout and how long unclaimed money has left.</li>
              <li><b className="text-ink">Approvals</b> holds every decision waiting for a person.</li>
              <li><b className="text-ink">Conflicts</b> holds payouts where the evidence disagrees.</li>
              <li><b className="text-ink">Batches</b> creates batches, imports CSVs and loads demo data.</li>
              <li><b className="text-ink">Benchmark</b> compares a careless retry script with PayoutRescue.</li>
              <li><b className="text-ink">Audit log</b> is the tamper-evident record of everything.</li>
            </ol>
          </div>
        </Section>

        {(shownTasks.length > 0 || !q) && (
          <Section id="tasks" title={`2. What you can do (${shownTasks.length} guides)`}>
            <div className="grid gap-4 md:grid-cols-2">
              {shownTasks.map((t, i) => (
                <article key={t.id} className="panel flex flex-col p-4 print:break-inside-avoid">
                  <h3 className="font-bold">{i + 1}. <Highlight text={t.title} q={q} /></h3>
                  <ol className="mt-2 flex-1 list-decimal space-y-1 pl-5 text-[13px] text-muted">{t.steps.map((s) => <li key={s}><Highlight text={s} q={q} /></li>)}</ol>
                  <div className="mt-3 flex gap-3 print:hidden">
                    {t.link && <Link to={t.link} className="font-semibold text-marine hover:underline">{t.linkLabel}</Link>}
                    {t.csv && <button className="font-semibold text-marine hover:underline" onClick={() => download('/api/audit/export.csv', 'audit_log.csv').catch((e: Error) => toast(e.message, 'error'))}>Download CSV</button>}
                  </div>
                </article>
              ))}
            </div>
          </Section>
        )}

        <Section id="payouts" title={`3. Every demo payout (${total || 19})`}>
          <p className="text-muted">These come straight from the server’s demo data, so this list cannot go out of date. The Now column is matched by email from your running server ({loaded} of {total} loaded).</p>
          {demo.error ? <ErrorNote error={demo.error} /> : demo.isLoading ? <Skeleton className="h-40" /> : (
            <>
              <h3 className="font-bold">Standard pack ({standard.length}): Batches, then Load demo data</h3>
              <DemoTable rows={standard} items={byEmail} />
              <h3 className="pt-3 font-bold">Conflict pack ({conflicts.length}): Batches, then Load conflict pack</h3>
              <DemoTable rows={conflicts} items={byEmail} showKind />
            </>
          )}
        </Section>

        <Section id="dictionary" title="4. Statuses and errors">
          <div className="grid gap-4 md:grid-cols-2">
            <div className="panel p-4">
              <h3 className="h-section mb-2">Statuses</h3>
              <ul className="space-y-2">{statusGuide.map(([s, text]) => <li key={s} className="grid grid-cols-[110px_1fr] items-center gap-2"><StatusChip status={s} /><span className="text-muted">{text}</span></li>)}</ul>
            </div>
            <div className="panel max-h-[420px] overflow-y-auto p-4">
              <h3 className="h-section mb-2">PayPal error names</h3>
              <dl className="space-y-2">{Object.entries(explanations).map(([code, text]) => <div key={code}><dt className="break-all font-mono text-[12px] font-semibold">{code}</dt><dd className="text-muted">{text}</dd></div>)}</dl>
            </div>
          </div>
        </Section>

        <Section id="rules" title="5. What the system proposes, and why">
          <p className="text-muted">This table is generated by running the real rules (<code>rules.Decide</code>) over every situation, so it always matches what the system does.</p>
          {reference.error ? <ErrorNote error={reference.error} /> : reference.isLoading ? <Skeleton className="h-40" /> : (
            <div className="panel max-h-[520px] overflow-auto print:max-h-none">
              <table className="w-full min-w-[640px] text-left">
                <thead className="sticky top-0 bg-sunken"><tr className="text-[12px] text-muted"><th className="px-3 py-2 font-semibold">When</th><th className="px-2 py-2 font-semibold">It proposes</th><th className="px-3 py-2 font-semibold">Why</th></tr></thead>
                <tbody>
                  {matrix.map((r) => { const p = proposalText(r); return (
                    <tr key={r.when} className="border-t border-line align-top"><td className="px-3 py-2 font-semibold"><Highlight text={r.when} q={q} /></td><td className="px-2 py-2">{p.what}</td><td className="px-3 py-2 text-muted">{p.why}</td></tr>
                  ) })}
                </tbody>
              </table>
            </div>
          )}
        </Section>

        <Section id="actions" title="6. Actions and approvals">
          <div className="grid gap-4 md:grid-cols-2">
            <div className="panel p-4" style={{ borderLeft: '4px solid var(--marine)' }}>
              <h3 className="font-bold">These move money</h3>
              <ul className="mt-2 list-disc space-y-1 pl-5 text-muted">
                <li><b className="text-ink">Cancel unclaimed payout:</b> takes the money back so the recipient can no longer claim it.</li>
                <li><b className="text-ink">Send one replacement:</b> pays the corrected email once.</li>
              </ul>
              <p className="mt-2 text-[13px] text-muted">Each needs your name, passing safety checks, and a fresh PayPal read. They can never be approved in bulk.</p>
            </div>
            <div className="panel p-4">
              <h3 className="font-bold">These do not</h3>
              <ul className="mt-2 list-disc space-y-1 pl-5 text-muted">
                <li><b className="text-ink">Message:</b> a draft from a fixed template. Never emailed.</li>
                <li><b className="text-ink">Needs a decision:</b> a person must look at it.</li>
                <li><b className="text-ink">For your information:</b> a note.</li>
              </ul>
              <p className="mt-2 text-[13px] text-muted">These can be acknowledged in bulk on the Approvals page.</p>
            </div>
          </div>
          <p className="text-muted">Safety checks on a replacement: the original is confirmed dead, no other live payout exists for this person, the corrected email is valid, the attempt limit is not reached, and amount and currency are copied from the payout record. Message templates: {(reference.data?.template_keys ?? []).slice().sort().join(', ') || '…'}.</p>
        </Section>

        {conflictKinds.length > 0 && (
          <Section id="conflicts" title={`7. Conflicts (${Object.keys(conflictInfo).length} kinds)`}>
            <div className="space-y-3">
              {conflictKinds.map((k) => (
                <article key={k} className="panel p-4 print:break-inside-avoid">
                  <div className="flex flex-wrap items-baseline justify-between gap-2"><h3 className="font-bold"><Highlight text={conflictInfo[k].title} q={q} /></h3><code className="text-[12px] text-muted">{k}</code></div>
                  <p className="mt-1"><Highlight text={conflictInfo[k].what} q={q} /></p>
                  <ul className="mt-2 space-y-0.5 text-[13px] text-muted">
                    {(reference.data?.conflict_resolutions[k] ?? []).map((r) => <li key={r}><b className="text-ink">{resolutionLabels[r]?.label ?? r}:</b> {resolutionLabels[r]?.help}</li>)}
                  </ul>
                </article>
              ))}
            </div>
          </Section>
        )}

        {shownInvariants.length > 0 && (
          <Section id="safety" title="8. Safety guarantees">
            <ol className="space-y-3">{shownInvariants.map(([title, text], i) => <li key={title} className="panel p-4 print:break-inside-avoid"><p className="font-bold" style={{ color: 'var(--pine)' }}>{i + 1}. <Highlight text={title} q={q} /></p><p className="mt-1 text-muted"><Highlight text={text} q={q} /></p></li>)}</ol>
          </Section>
        )}

        <Section id="screens" title="9. Screens and buttons">
          <dl className="panel divide-y divide-line">
            {[
              ['Payouts', 'Every payout. Filter by status or source, search, see conflicts, export CSV, click a row to review it.'],
              ['A payout page', 'Summary, 30-day rail, conflict banner, decisions waiting, conversation, reply simulator, corrected email box, notes, attempts, history, Read back from PayPal, Print report.'],
              ['Approvals', 'Every decision waiting, money-moving first. Snooze, reject, approve, or tick messages to acknowledge several at once.'],
              ['Conflicts', 'Payouts where evidence disagrees, with one button per allowed resolution. Open and Resolved tabs.'],
              ['Batches', 'Create a batch, import a CSV with a preview, and load the demo and conflict packs.'],
              ['Benchmark', 'Run the scripted comparison and read the results by scenario.'],
              ['Audit log', 'Every recorded event with its hash, the chain integrity tag, and Export CSV.'],
              ['Setup', 'Connection status, the webhook URL, fake-time buttons, Reconcile now, and the PayPal test values in use.'],
              ['Left menu, bottom', 'Acting as (your name for approvals), light or dark mode, and Sign out.'],
              ['Top bar', 'The ? button opens help for the current page. Ctrl+K opens the command palette.'],
            ].map(([name, text]) => <div key={name} className="grid gap-1 p-3 md:grid-cols-[170px_1fr]"><dt className="font-semibold">{name}</dt><dd className="text-muted">{text}</dd></div>)}
          </dl>
        </Section>

        <Section id="api" title="10. API and webhook reference">
          <p className="text-muted">Generated from <code>GET /api/reference</code>. Every route is registered together with its description, so this list cannot drift from what the server really serves. The webhook is <code>POST /api/webhooks/paypal</code>.</p>
          {reference.isLoading ? <Skeleton className="h-40" /> : reference.error ? <ErrorNote error={reference.error} /> : (
            <div className="panel max-h-[480px] overflow-auto print:max-h-none">
              <table className="w-full min-w-[640px] text-left">
                <thead className="sticky top-0 bg-sunken"><tr className="text-[12px] text-muted"><th className="px-3 py-2 font-semibold">Route</th><th className="px-3 py-2 font-semibold">What it does</th></tr></thead>
                <tbody>{reference.data?.routes.filter((r) => has(r.pattern, r.doc)).map((r) => <tr key={r.pattern} className="border-t border-line align-top"><td className="whitespace-nowrap px-3 py-2 font-mono text-[12px] font-semibold"><Highlight text={r.pattern} q={q} /></td><td className="px-3 py-2 text-muted"><Highlight text={r.doc} q={q} /></td></tr>)}</tbody>
              </table>
            </div>
          )}
          {reference.data && <p className="text-[13px] text-muted">PayPal test values: {Object.entries(reference.data.sim_tags).map(([k, v]) => `${k} (${v})`).join(', ')}.</p>}
        </Section>

        <Section id="keys" title="11. Keyboard">
          <dl className="panel divide-y divide-line">{shortcuts.map(([k, text]) => <div key={k} className="flex items-center justify-between gap-3 p-3"><dd className="text-muted">{text}</dd><dt><kbd className="rounded border border-line bg-sunken px-2 py-0.5 font-mono text-[12px]">{k}</kbd></dt></div>)}</dl>
        </Section>

        {shownGlossary.length > 0 && (
          <Section id="glossary" title="12. Glossary">
            <dl className="panel divide-y divide-line">{shownGlossary.map(([term, text]) => <div key={term} className="grid gap-1 p-3 md:grid-cols-[200px_1fr]"><dt className="font-semibold"><Highlight text={term} q={q} /></dt><dd className="text-muted"><Highlight text={text} q={q} /></dd></div>)}</dl>
          </Section>
        )}

        {shownFaq.length > 0 && (
          <Section id="faq" title="13. FAQ and troubleshooting">
            <div className="space-y-3">{shownFaq.map(([a, b]) => <div key={a} className="panel p-4 print:break-inside-avoid"><p className="font-semibold"><Highlight text={a} q={q} /></p><p className="mt-1 text-muted"><Highlight text={b} q={q} /></p></div>)}</div>
          </Section>
        )}

        {q && shownTasks.length + shownInvariants.length + conflictKinds.length + shownFaq.length + shownGlossary.length + matrix.length === 0 && (
          <p className="rounded-lg border border-dashed border-line p-6 text-center text-muted">Nothing matches “{search}”. Try a shorter word.</p>
        )}
      </div>
    </div>
  )
}
