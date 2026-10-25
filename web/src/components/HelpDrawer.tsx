import { useEffect, useRef } from 'react'
import { Link } from 'react-router-dom'

interface PageHelp { title: string; section: string; tips: string[] }

function helpFor(path: string): PageHelp {
  if (path.startsWith('/items/')) return { title: 'A payout', section: 'tasks', tips: ['Read the summary at the top, then Waiting on you.', 'Money-moving decisions show safety checks and the exact request. Approve needs your name.', 'Simulate a reply to see how replies are read. Attack presets are blocked.', 'Add notes, or print a one-page report.'] }
  switch (path) {
    case '/approvals': return { title: 'Approvals', section: 'actions', tips: ['Cancels and replacements move money. They are approved one at a time.', 'Tick messages and notes to acknowledge several at once.', 'Snooze hides a decision for 1, 3 or 7 days.'] }
    case '/conflicts': return { title: 'Conflicts', section: 'conflicts', tips: ['A conflict means the evidence disagrees, so a person decides.', 'Resolving never sends money by itself.', 'Some choices need a note. Your name is recorded.'] }
    case '/batches': return { title: 'Batches', section: 'tasks', tips: ['Fake needs no PayPal account. Simulated and Live sandbox need sandbox credentials.', 'Import a CSV (name, email, amount, currency) and read the preview first.', 'Load demo data and Load conflict pack fill the app with examples.'] }
    case '/benchmark': return { title: 'Benchmark', section: 'tasks', tips: ['Scripted scenarios, not real-world results.', 'Each scenario is labelled Fake, Simulated or Live sandbox.', 'The manual-step baseline is an assumption.'] }
    case '/audit': return { title: 'Audit log', section: 'safety', tips: ['Each entry contains the hash of the one before it.', 'Chain intact means nothing was edited or removed.', 'Export CSV downloads every entry.'] }
    case '/setup': return { title: 'Setup', section: 'tasks', tips: ['See which connections are ready.', 'Move fake time forward to see day 20, 27 and 30.', 'Reconcile now reads every unfinished payout from PayPal.'] }
    case '/help': return { title: 'Help and guide', section: 'start', tips: ['Search to filter and highlight.', 'Every table here is generated from the running server.'] }
    default: return { title: 'Payouts', section: 'start', tips: ['Click a row to review a payout.', 'The sentence at the top is the summary: money in limbo and decisions waiting.', 'Filter by status or source, search, or export CSV.'] }
  }
}

export function HelpDrawer({ open, onClose, path }: { open: boolean; onClose: () => void; path: string }) {
  const closeRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (open) closeRef.current?.focus()
  }, [open])
  if (!open) return null
  const h = helpFor(path)
  return (
    <div className="fixed inset-0 z-40 print:hidden" onClick={onClose}>
      <div className="absolute inset-0 bg-black/30" />
      <aside role="dialog" aria-modal="true" aria-label={`Help: ${h.title}`} className="rise absolute right-0 top-0 h-full w-[min(380px,92vw)] overflow-y-auto border-l border-line bg-surface p-5 shadow-lift" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-start justify-between gap-2">
          <div><p className="h-section">Help for this page</p><h2 className="text-lg font-bold">{h.title}</h2></div>
          <button ref={closeRef} className="btn btn-quiet" onClick={onClose} aria-label="Close help">Close</button>
        </div>
        <ul className="mt-4 list-disc space-y-2 pl-5">{h.tips.map((t) => <li key={t}>{t}</li>)}</ul>
        <Link className="btn btn-primary mt-5" to={`/help#${h.section}`} onClick={onClose}>Open the full guide</Link>
        <p className="mt-4 text-[12px] text-muted">Press <kbd className="rounded border border-line bg-sunken px-1.5 font-mono">Esc</kbd> to close.</p>
      </aside>
    </div>
  )
}
