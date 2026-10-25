import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, listConflicts, resolveConflict } from '../lib/api'
import type { Conflict, Item } from '../lib/types'
import { Empty, ErrorNote, ModeBadge, Skeleton, StatusChip, useToast } from '../components/ui'
import { money, timeAgo } from '../lib/format'

export const conflictInfo: Record<string, { title: string; what: string; color: string }> = {
  claim_race: { title: 'Claimed during a cancel', color: 'var(--signal)', what: 'The recipient claimed the money before a cancel or replacement could be approved. The stale proposal was withdrawn automatically, and they were paid once.' },
  ambiguous_reply: { title: 'Reply with two emails', color: 'var(--signal)', what: 'The recipient wrote more than one email address. Nothing was changed, because guessing could send money to the wrong person.' },
  email_same_as_on_file: { title: 'Same email as before', color: 'var(--signal)', what: 'The “corrected” email is the one we already have, so sending again would fail the same way.' },
  refusal_vs_proposal: { title: 'Recipient declines', color: 'var(--brick)', what: 'The recipient said they do not want the payment, yet a cancel or replacement is still waiting for approval.' },
  held_with_correction: { title: 'On hold but corrected', color: 'var(--iris)', what: 'PayPal is still holding the payment, so no replacement may be sent, even though a corrected email exists.' },
  attempts_exhausted: { title: 'Three attempts used', color: 'var(--brick)', what: 'This payout has used all of its attempts. The system never sends a fourth one by itself.' },
  status_disagreement: { title: 'PayPal and records disagree', color: 'var(--brick)', what: 'PayPal reported a status change that cannot happen. It was ignored and the payout was flagged for review.' },
}

export const resolutionLabels: Record<string, { label: string; help: string }> = {
  acknowledge: { label: 'Acknowledge', help: 'They were paid once. Close this.' },
  use_email: { label: 'Use the selected email', help: 'Records it as the corrected email. A replacement, if any, still needs its own approval.' },
  ask_again: { label: 'Draft a follow-up message', help: 'Creates a message draft asking the recipient to clarify. Nothing is emailed.' },
  dismiss: { label: 'Dismiss', help: 'Close without changing anything.' },
  withdraw_proposals: { label: 'Withdraw the open proposals', help: 'Rejects the waiting cancel or replacement, because the recipient declined.' },
  keep_proposals: { label: 'Keep the proposals', help: 'Leave them waiting. A note explaining why is required.' },
  wait: { label: 'Wait for PayPal', help: 'Close this and let PayPal finish. Nothing is re-sent.' },
  escalate: { label: 'Escalate', help: 'Creates a “needs a human decision” item for compliance.' },
  close_unpaid: { label: 'Close as unpaid', help: 'Stop trying. No further attempt is sent.' },
  paid_manually: { label: 'Record that it was paid by hand', help: 'For a bank transfer or similar. A note is required.' },
  refresh_now: { label: 'Read back from PayPal now', help: 'Closes only if PayPal’s answer is now consistent.' },
  accept_and_close: { label: 'Accept the current state', help: 'You investigated; clear the review flag and close.' },
}

const needsNote = (kind: string, res: string) => (kind === 'refusal_vs_proposal' && res === 'keep_proposals') || (kind === 'attempts_exhausted' && res === 'paid_manually')

function ConflictCard({ c, item, allowed, approver }: { c: Conflict; item?: Item; allowed: string[]; approver: string }) {
  const qc = useQueryClient()
  const toast = useToast()
  const info = conflictInfo[c.kind] ?? { title: c.kind, what: c.summary, color: 'var(--muted)' }
  const candidates = Array.isArray((c.details as { candidates?: unknown }).candidates) ? ((c.details as { candidates: string[] }).candidates) : []
  const [chosen, setChosen] = useState('')
  const [note, setNote] = useState('')
  const [pending, setPending] = useState<string | null>(null)

  const resolve = useMutation({
    mutationFn: (resolution: string) => resolveConflict(c.id, { resolution, approver, value: resolution === 'use_email' ? chosen : undefined, note: note.trim() || undefined }),
    onSuccess: () => { toast('Conflict resolved and recorded.'); void qc.invalidateQueries() },
    onError: (e: Error) => toast(e.message, 'error'),
    onSettled: () => setPending(null),
  })

  const open = c.status === 'open'
  return (
    <article className="panel rise p-4" style={{ borderLeft: `4px solid ${open ? info.color : 'var(--line)'}` }}>
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="font-bold">{info.title}</h3>
        <span className="text-[12px] text-muted">{open ? `Opened ${timeAgo(c.created_at)}` : `Resolved ${c.resolved_at ? timeAgo(c.resolved_at) : ''}`}</span>
      </header>
      <p className="mt-1">{info.what}</p>

      {item && (
        <div className="mt-3 flex flex-wrap items-center gap-2 rounded-md bg-sunken p-2.5">
          <Link className="font-semibold text-marine hover:underline" to={`/items/${item.id}`}>{item.recipient_name}</Link>
          <span className="num font-semibold">{money(item.amount, item.currency)}</span>
          <StatusChip status={item.status} />
          <ModeBadge mode={item.mode} />
          <span className="text-muted">{item.recipient_email}{item.corrected_email ? ` → ${item.corrected_email}` : ''}</span>
          <span className="text-muted">Try {item.attempt}</span>
        </div>
      )}
      <p className="mt-2 text-[13px] text-muted">{c.summary}</p>

      {open ? (
        <div className="mt-3 space-y-3">
          {c.kind === 'ambiguous_reply' && candidates.length > 0 && (
            <fieldset>
              <legend className="mb-1 font-semibold">Which email is right?</legend>
              <div className="space-y-1">
                {candidates.map((email) => (
                  <label key={email} className="flex cursor-pointer items-center gap-2">
                    <input type="radio" name={`cand-${c.id}`} checked={chosen === email} onChange={() => setChosen(email)} />
                    <span className="font-mono text-[13px]">{email}</span>
                  </label>
                ))}
              </div>
            </fieldset>
          )}
          <label className="block">
            <span className="mb-1 block font-semibold">Note <span className="font-normal text-muted">(required for some choices)</span></span>
            <input className="field" value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} placeholder="Why you chose this" />
          </label>
          <div className="flex flex-wrap gap-2">
            {allowed.map((res) => {
              const meta = resolutionLabels[res] ?? { label: res, help: '' }
              const blocked = !approver.trim() || (res === 'use_email' && !chosen) || (needsNote(c.kind, res) && !note.trim())
              return (
                <button key={res} className={res === 'dismiss' || res === 'wait' ? 'btn btn-quiet' : 'btn btn-primary'} title={meta.help} disabled={blocked || resolve.isPending}
                  onClick={() => { setPending(res); resolve.mutate(res) }}>
                  {resolve.isPending && pending === res ? 'Working…' : meta.label}
                </button>
              )
            })}
          </div>
          <ul className="space-y-0.5 text-[12px] text-muted">
            {allowed.map((res) => <li key={res}><b className="text-ink">{resolutionLabels[res]?.label ?? res}:</b> {resolutionLabels[res]?.help}</li>)}
          </ul>
          {!approver.trim() && <p className="text-[12px] text-muted">Enter your name in Acting as (left menu) to resolve.</p>}
        </div>
      ) : (
        <p className="mt-3 rounded-md bg-sunken p-2.5 text-[13px]">
          <b>{resolutionLabels[c.resolution ?? '']?.label ?? c.resolution}</b> by {c.resolved_by}{c.note ? `. “${c.note}”` : ''}
        </p>
      )}
    </article>
  )
}

export function Conflicts({ approver }: { approver: string }) {
  const [tab, setTab] = useState<'open' | 'resolved'>('open')
  const conflicts = useQuery({ queryKey: ['conflicts', tab], queryFn: () => listConflicts(tab) })
  const items = useQuery({ queryKey: ['items'], queryFn: () => api<Item[]>('/api/items') })
  const ref = useQuery({ queryKey: ['reference'], queryFn: () => api<{ conflict_resolutions: Record<string, string[]> }>('/api/reference') })
  const byId = new Map((items.data ?? []).map((i) => [i.id, i]))

  return (
    <div className="space-y-4">
      <section className="panel p-5">
        <h2 className="text-lg font-bold">Conflicts</h2>
        <p className="mt-1 max-w-3xl text-muted">A conflict is a payout where the evidence disagrees, so a person must decide. Resolving one never sends, cancels or re-sends money by itself. Anything further still needs its own approval.</p>
        <div role="tablist" aria-label="Conflict status" className="mt-3 flex gap-2">
          {(['open', 'resolved'] as const).map((t) => (
            <button key={t} role="tab" aria-selected={tab === t} onClick={() => setTab(t)} className={`rounded-full border px-3 py-1 font-semibold ${tab === t ? 'border-marine bg-marine text-marine-ink' : 'border-line bg-surface hover:bg-sunken'}`}>
              {t === 'open' ? 'Open' : 'Resolved'}{conflicts.data && tab === t ? ` · ${conflicts.data.length}` : ''}
            </button>
          ))}
        </div>
      </section>
      {conflicts.isLoading ? <Skeleton className="h-48" /> : conflicts.error ? <ErrorNote error={conflicts.error} /> : (conflicts.data?.length ?? 0) === 0 ? (
        <Empty title={tab === 'open' ? 'No open conflicts' : 'Nothing resolved yet'}>
          {tab === 'open' ? <>Use Load conflict pack on the Batches page to see all seven kinds.</> : null}
        </Empty>
      ) : (
        <div className="space-y-3">
          {conflicts.data!.map((c) => (
            <ConflictCard key={c.id} c={c} item={byId.get(c.item_id)} approver={approver} allowed={ref.data?.conflict_resolutions[c.kind] ?? []} />
          ))}
        </div>
      )}
    </div>
  )
}
