import { useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'
import { explain, plural } from '../lib/format'
import type { ItemDetail, Summary } from '../lib/types'

/* ------------------------------------------------------------------ content */

interface Faq { q: string; a: string }
interface PageHelp {
  key: string
  title: string
  blurb: string
  section: string // anchor in the full guide
  tips: string[]
  faq: Faq[]
  keys: [string, string][]
}

const GLOBAL_KEYS: [string, string][] = [
  ['Ctrl / ⌘ + K', 'Open the command palette'],
  ['?', 'Open or close this help'],
  ['g then p / a / c / b', 'Go to Payouts, Approvals, Conflicts or Batches'],
  ['Esc', 'Close a dialog'],
]

const PAGES: Record<string, PageHelp> = {
  payouts: {
    key: 'payouts', title: 'Payouts', section: 'start',
    blurb: 'Every payout you sent, what happened to it, and how long unclaimed money has left.',
    tips: ['Click a row, or its Review link, to open a payout.', 'The sentence at the top is the summary: money in limbo and decisions waiting.', 'Filter by status or source, search by name or email, or export CSV.', 'The Conflict column shows payouts where the evidence disagrees.'],
    faq: [
      { q: 'What does “in limbo” mean?', a: 'Money that has left your balance but has not reached the recipient: unclaimed, on hold, blocked or pending.' },
      { q: 'Why do some payouts have a rail?', a: 'Only unclaimed money is on PayPal’s 30-day clock. The rail shows how far along it is.' },
    ],
    keys: [['Tab', 'Move between filters, the table and links']],
  },
  item: {
    key: 'item', title: 'A payout', section: 'tasks',
    blurb: 'One payout in detail: what happened, what is waiting for you, and the full history.',
    tips: ['Read the summary at the top, then Waiting on you.', 'Money-moving decisions show safety checks and the exact request. Approve needs your name.', 'Simulate a reply to see how replies are read. Attack presets are blocked.', 'Add notes, or print a one-page report.'],
    faq: [
      { q: 'Why is Approve disabled?', a: 'Either your name is empty (see Acting as) or a safety check is failing. The card says which.' },
      { q: 'What does “refused safely” mean?', a: 'PayPal was read again and the facts changed, for example the recipient claimed the money. Nothing was sent.' },
      { q: 'Can I approve twice?', a: 'No. The second attempt is refused, and only one replacement can ever be live.' },
    ],
    keys: [['Tab', 'Move through the safety checks and buttons']],
  },
  approvals: {
    key: 'approvals', title: 'Approvals', section: 'actions',
    blurb: 'Every decision waiting for a person, money-moving ones first.',
    tips: ['Cancels and replacements move money. They are approved one at a time.', 'Tick messages and notes to acknowledge several at once.', 'Snooze hides a decision for 1, 3 or 7 days.'],
    faq: [
      { q: 'Why is there no checkbox next to a cancel?', a: 'Anything that moves money must be approved one at a time, on purpose.' },
      { q: 'Where did a snoozed decision go?', a: 'It is hidden until the snooze ends, then returns to the queue. Nothing was approved.' },
    ],
    keys: [['Space', 'Tick the focused checkbox']],
  },
  conflicts: {
    key: 'conflicts', title: 'Conflicts', section: 'conflicts',
    blurb: 'Payouts where the evidence disagrees, so a person decides.',
    tips: ['A conflict means the evidence disagrees, so a person decides.', 'Resolving never sends money by itself.', 'Some choices need a note. Your name is recorded.'],
    faq: [
      { q: 'Can a resolution pay someone?', a: 'No. At most it records a validated email or creates an ordinary proposal, which still needs its own approval.' },
      { q: 'Why can’t I choose “use the selected email”?', a: 'Pick one of the listed emails first. Anything else is refused.' },
    ],
    keys: [['g then c', 'Come back here from anywhere']],
  },
  batches: {
    key: 'batches', title: 'Batches', section: 'tasks',
    blurb: 'Create a batch, import a CSV, or load the demo data.',
    tips: ['Fake needs no PayPal account. Simulated and Live sandbox need sandbox credentials.', 'Import a CSV (name, email, amount, currency) and read the preview first.', 'Load demo data and Load conflict pack fill the app with examples.'],
    faq: [
      { q: 'Why was I warned about a duplicate?', a: 'A recipient already received the same amount in the last 7 days. Confirm only if a second payment is intended.' },
      { q: 'Why was a CSV row rejected?', a: 'The preview lists the reason (bad email, bad amount, repeated line). Rejected rows are never imported.' },
    ],
    keys: [],
  },
  benchmark: {
    key: 'benchmark', title: 'Benchmark', section: 'tasks',
    blurb: 'A scripted comparison between a careless retry script and PayoutRescue.',
    tips: ['Scripted scenarios, not real-world results.', 'Each scenario is labelled Fake, Simulated or Live sandbox.', 'The manual-step baseline is an assumption.'],
    faq: [{ q: 'Is this a real measurement?', a: 'No. It shows how each approach behaves in the same scripted situations.' }],
    keys: [],
  },
  audit: {
    key: 'audit', title: 'Audit log', section: 'safety',
    blurb: 'The tamper-evident record of everything the system did.',
    tips: ['Each entry contains the hash of the one before it.', 'Chain intact means nothing was edited or removed.', 'Export CSV downloads every entry.'],
    faq: [{ q: 'Who approved something?', a: 'Every approval, rejection and resolution stores the name that was in Acting as.' }],
    keys: [],
  },
  setup: {
    key: 'setup', title: 'Setup', section: 'tasks',
    blurb: 'Connections, the webhook URL, and the time controls.',
    tips: ['See which connections are ready.', 'Move fake time forward to see day 20, 27 and 30.', 'Reconcile now reads every unfinished payout from PayPal.'],
    faq: [
      { q: 'Can I move real PayPal time?', a: 'No. The time buttons only affect Fake items.' },
      { q: 'Why are webhooks rejected?', a: 'PAYPAL_WEBHOOK_ID is missing or wrong. PayPal’s simulator events cannot be verified.' },
    ],
    keys: [],
  },
  help: {
    key: 'help', title: 'Help and guide', section: 'start',
    blurb: 'The full guide.',
    tips: ['Search to filter and highlight.', 'Every table here is generated from the running server.'],
    faq: [],
    keys: [['/', 'Focus the search box']],
  },
}

function pageKey(path: string): string {
  if (path.startsWith('/items/')) return 'item'
  const k = path.replace(/^\//, '').split('/')[0]
  return k === '' ? 'payouts' : k in PAGES ? k : 'payouts'
}

/* ------------------------------------------------------------------ live context */

interface Step { tone: 'warn' | 'action' | 'info'; title: string; body: string; to?: string; cta?: string }

function nextSteps(key: string, summary?: Summary, detail?: ItemDetail): Step[] {
  const steps: Step[] = []
  if (detail) {
    const it = detail.item
    const open = detail.actions.filter((a) => a.status === 'proposed')
    const money = open.filter((a) => a.kind === 'cancel' || a.kind === 'reissue')
    const conflicts = (detail.conflicts ?? []).filter((c) => c.status === 'open')
    if (conflicts.length) steps.push({ tone: 'warn', title: `${plural(conflicts.length, 'conflict')} on this payout`, body: 'A person must decide. Resolving never sends money by itself.', to: '/conflicts', cta: 'Open Conflicts' })
    if (money.length) steps.push({ tone: 'action', title: `${plural(money.length, 'decision')} that moves money`, body: 'Check every safety check, read the exact request, then approve with your name. PayPal is read again first.' })
    if (open.length > money.length) steps.push({ tone: 'info', title: `${plural(open.length - money.length, 'message or note')} waiting`, body: 'Read the drafted message and mark it as sent. Nothing is emailed.' })
    const why = explain(it.status, it.error_code)
    switch (it.status) {
      case 'UNCLAIMED': steps.push({ tone: 'info', title: `Day ${it.age_days} of 30`, body: `${it.days_left} days before PayPal returns this money. A reminder is drafted at day 20 and a cancel is proposed at day 27.` }); break
      case 'FAILED': steps.push({ tone: 'info', title: 'Why it failed', body: `${why} Enter a corrected email or simulate the recipient’s reply to continue.` }); break
      case 'ONHOLD': case 'BLOCKED': steps.push({ tone: 'info', title: 'PayPal has it', body: 'It is never re-sent while PayPal holds or blocks it. Wait, or escalate.' }); break
      case 'SUCCESS': steps.push({ tone: 'info', title: 'Paid', body: 'Nothing to do.' }); break
      case 'RETURNED': case 'CANCELED': steps.push({ tone: 'info', title: 'Money is back with you', body: 'A replacement needs a valid corrected email.' }); break
    }
  }
  if (summary) {
    if (summary.needing_action > 0 && key !== 'approvals') steps.push({ tone: 'action', title: `${plural(summary.needing_action, 'decision')} waiting`, body: 'Across all payouts.', to: '/approvals', cta: 'Open Approvals' })
    if ((summary.open_conflicts ?? 0) > 0 && key !== 'conflicts' && !detail) steps.push({ tone: 'warn', title: `${plural(summary.open_conflicts ?? 0, 'open conflict')}`, body: 'Payouts where the evidence disagrees.', to: '/conflicts', cta: 'Open Conflicts' })
    if (summary.next_return_days !== null && summary.next_return_days <= 5 && key === 'payouts') steps.push({ tone: 'warn', title: `PayPal returns money in ${plural(summary.next_return_days, 'day')}`, body: 'Cancel or contact the recipient before then.' })
  }
  return steps
}

const toneColor: Record<Step['tone'], string> = { warn: 'var(--brick)', action: 'var(--marine)', info: 'var(--line)' }

/* ------------------------------------------------------------------ component */

type Tab = 'page' | 'ask' | 'keys'
const TABS: { id: Tab; label: string }[] = [{ id: 'page', label: 'This page' }, { id: 'ask', label: 'Questions' }, { id: 'keys', label: 'Shortcuts' }]
const TAB_KEY = 'payoutrescue.helptab'
const FOCUSABLE = 'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),summary,[tabindex]:not([tabindex="-1"])'

function Highlight({ text, q }: { text: string; q: string }): ReactNode {
  if (!q) return text
  const i = text.toLowerCase().indexOf(q)
  if (i < 0) return text
  return <>{text.slice(0, i)}<mark className="rounded bg-signal/30 px-0.5 text-ink">{text.slice(i, i + q.length)}</mark>{text.slice(i + q.length)}</>
}

export function HelpDrawer({ open, onClose, path }: { open: boolean; onClose: () => void; path: string }) {
  const titleId = useId()
  const panelRef = useRef<HTMLElement>(null)
  const closeRef = useRef<HTMLButtonElement>(null)
  const returnTo = useRef<HTMLElement | null>(null)
  const [tab, setTab] = useState<Tab>(() => (sessionStorage.getItem(TAB_KEY) as Tab | null) ?? 'page')
  const [search, setSearch] = useState('')

  const key = pageKey(path)
  const page = PAGES[key]
  const itemId = key === 'item' ? path.split('/')[2] ?? '' : ''

  const summary = useQuery({ queryKey: ['limbo'], queryFn: () => api<Summary>('/api/limbo'), enabled: open })
  const detail = useQuery({ queryKey: ['item', itemId], queryFn: () => api<ItemDetail>(`/api/items/${itemId}`), enabled: open && !!itemId })
  const steps = useMemo(() => nextSteps(key, summary.data, itemId ? detail.data : undefined), [key, summary.data, detail.data, itemId])

  const choose = useCallback((t: Tab) => { setTab(t); sessionStorage.setItem(TAB_KEY, t) }, [])

  // Focus management: move focus in, lock page scroll, give focus back on close.
  useEffect(() => {
    if (!open) return
    returnTo.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    closeRef.current?.focus()
    setSearch('')
    return () => {
      document.body.style.overflow = prev
      returnTo.current?.focus?.()
    }
  }, [open])

  const q = search.trim().toLowerCase()
  const has = (...t: string[]) => !q || t.some((x) => x.toLowerCase().includes(q))
  const tips = page.tips.filter((t) => has(t))
  const faq = page.faq.filter((f) => has(f.q, f.a))
  const keys = [...page.keys, ...GLOBAL_KEYS].filter(([k, d]) => has(k, d))
  const nothing = q !== '' && tips.length + faq.length + keys.length === 0

  const onKeyDown = (e: KeyboardEvent<HTMLElement>) => {
    if (e.key === 'Escape') { e.stopPropagation(); onClose(); return }
    if (e.key !== 'Tab' || !panelRef.current) return
    const els = [...panelRef.current.querySelectorAll<HTMLElement>(FOCUSABLE)].filter((el) => el.offsetParent !== null)
    if (els.length === 0) return
    const first = els[0]
    const last = els[els.length - 1]
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus() }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus() }
  }
  const onTabKey = (e: KeyboardEvent<HTMLButtonElement>) => {
    const i = TABS.findIndex((t) => t.id === tab)
    const next = e.key === 'ArrowRight' ? (i + 1) % TABS.length : e.key === 'ArrowLeft' ? (i + TABS.length - 1) % TABS.length : -1
    if (next >= 0) { e.preventDefault(); choose(TABS[next].id); document.getElementById(`${titleId}-tab-${TABS[next].id}`)?.focus() }
  }

  if (!open) return null
  return (
    <div className="fixed inset-0 z-40 print:hidden" onClick={onClose}>
      <div className="absolute inset-0 bg-black/30" aria-hidden="true" />
      <aside
        ref={panelRef} role="dialog" aria-modal="true" aria-labelledby={titleId} onKeyDown={onKeyDown} onClick={(e) => e.stopPropagation()}
        className="slide-in absolute right-0 top-0 flex h-full w-[min(420px,94vw)] flex-col border-l border-line bg-surface shadow-lift"
      >
        <header className="border-b border-line p-5 pb-3">
          <div className="flex items-start justify-between gap-2">
            <div><p className="h-section">Help for this page</p><h2 id={titleId} className="text-lg font-bold">{page.title}</h2></div>
            <button ref={closeRef} className="btn btn-quiet" onClick={onClose} aria-label="Close help">Close</button>
          </div>
          <p className="mt-1 text-muted">{page.blurb}</p>
          <input type="search" className="field mt-3" placeholder="Search this help" aria-label="Search this help" value={search} onChange={(e) => setSearch(e.target.value)} />
          <div role="tablist" aria-label="Help sections" className="mt-3 flex gap-1.5">
            {TABS.map((t) => (
              <button key={t.id} id={`${titleId}-tab-${t.id}`} role="tab" aria-selected={tab === t.id} aria-controls={`${titleId}-panel`} tabIndex={tab === t.id ? 0 : -1}
                onClick={() => choose(t.id)} onKeyDown={onTabKey}
                className={`rounded-full border px-3 py-1 text-[13px] font-semibold ${tab === t.id ? 'border-marine bg-marine text-marine-ink' : 'border-line hover:bg-sunken'}`}>
                {t.label}
              </button>
            ))}
          </div>
        </header>

        <div id={`${titleId}-panel`} role="tabpanel" aria-labelledby={`${titleId}-tab-${tab}`} className="flex-1 space-y-5 overflow-y-auto p-5">
          {nothing && <p className="rounded-lg border border-dashed border-line p-5 text-center text-muted">Nothing here matches “{search}”. Try the full guide.</p>}

          {tab === 'page' && !q && steps.length > 0 && (
            <section aria-label="What needs you right now" className="space-y-2">
              <h3 className="h-section">Right now</h3>
              {steps.map((s) => (
                <div key={s.title} className="rounded-md border border-line bg-bg p-3" style={{ borderLeft: `4px solid ${toneColor[s.tone]}` }}>
                  <p className="font-semibold">{s.title}</p>
                  <p className="text-[13px] text-muted">{s.body}</p>
                  {s.to && <Link className="mt-1.5 inline-block font-semibold text-marine hover:underline" to={s.to} onClick={onClose}>{s.cta}</Link>}
                </div>
              ))}
            </section>
          )}

          {tab === 'page' && tips.length > 0 && (
            <section aria-label="Tips" className="space-y-2">
              <h3 className="h-section">What to do here</h3>
              <ul className="list-disc space-y-2 pl-5">{tips.map((t) => <li key={t}><Highlight text={t} q={q} /></li>)}</ul>
            </section>
          )}

          {tab === 'ask' && (
            <section aria-label="Questions" className="space-y-2">
              {faq.length === 0 && !nothing && <p className="text-muted">No questions for this page yet. Open the full guide for more.</p>}
              {faq.map((f) => (
                <details key={f.q} className="panel p-3" open={q !== ''}>
                  <summary className="cursor-pointer font-semibold"><Highlight text={f.q} q={q} /></summary>
                  <p className="mt-2 text-muted"><Highlight text={f.a} q={q} /></p>
                </details>
              ))}
            </section>
          )}

          {tab === 'keys' && keys.length > 0 && (
            <section aria-label="Keyboard shortcuts">
              <dl className="panel divide-y divide-line">
                {keys.map(([k, d]) => (
                  <div key={k + d} className="flex items-center justify-between gap-3 p-3"><dd className="text-muted"><Highlight text={d} q={q} /></dd><dt><kbd className="whitespace-nowrap rounded border border-line bg-sunken px-2 py-0.5 font-mono text-[12px]">{k}</kbd></dt></div>
                ))}
              </dl>
            </section>
          )}
        </div>

        <footer className="flex flex-wrap items-center gap-2 border-t border-line p-4">
          <Link className="btn btn-primary" to={`/help#${page.section}`} onClick={onClose}>Open the full guide</Link>
          <button className="btn btn-quiet" onClick={() => { localStorage.removeItem('payoutrescue.tour'); window.dispatchEvent(new Event('payoutrescue:tour')); onClose() }}>Take the tour</button>
          <span className="ml-auto text-[12px] text-muted">Esc to close</span>
        </footer>
      </aside>
    </div>
  )
}