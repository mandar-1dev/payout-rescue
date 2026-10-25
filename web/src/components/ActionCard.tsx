import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api, snoozeAction } from '../lib/api'
import { actionLabel, money } from '../lib/format'
import type { Action, Message, Preflight } from '../lib/types'
import { useToast } from './ui'

const MONEY = new Set(['cancel', 'reissue'])

interface ApproveResponse { action?: Action; message?: string }

export function ActionCard({ action, preflight, message, approver, heading }: {
  action: Action
  preflight?: Preflight
  message?: Message
  approver: string
  heading?: string
}) {
  const qc = useQueryClient()
  const toast = useToast()
  const isMoney = MONEY.has(action.kind)
  const proposed = action.status === 'proposed'
  const ready = !isMoney || (preflight?.ready ?? false)

  const approve = useMutation({
    mutationFn: () => api<ApproveResponse>(`/api/actions/${action.id}/approve`, { method: 'POST', json: { approver } }),
    onSuccess: (r) => {
      const a = r.action
      if (a?.status === 'failed') toast(`Refused safely: ${String(a.result?.error ?? 'the safety checks did not pass')}`, 'warn')
      else if (a?.status === 'approved') toast(r.message ?? 'Approved. PayPal was unreachable, so the same request will be retried safely.', 'warn')
      else toast(isMoney ? 'Approved and executed.' : 'Done.', 'ok')
      void qc.invalidateQueries()
    },
    onError: (e: Error) => toast(e.message, 'error'),
  })
  const reject = useMutation({
    mutationFn: () => api(`/api/actions/${action.id}/reject`, { method: 'POST', json: { approver } }),
    onSuccess: () => { toast('Rejected.'); void qc.invalidateQueries() },
    onError: (e: Error) => toast(e.message, 'error'),
  })

  const snooze = useMutation({
    mutationFn: (days: number) => snoozeAction(action.id, days),
    onSuccess: (_r, days) => { toast(`Snoozed for ${days} ${days === 1 ? 'day' : 'days'}. It returns to the queue afterwards.`); void qc.invalidateQueries() },
    onError: (e: Error) => toast(e.message, 'error'),
  })

  const tone = action.status === 'failed' ? 'var(--brick)' : isMoney ? 'var(--marine)' : 'var(--line)'
  return (
    <article className="panel rise p-4" style={{ borderLeft: `4px solid ${tone}` }}>
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="font-semibold">{heading ?? actionLabel[action.kind] ?? action.kind}</h3>
        <span className="text-[12px] capitalize text-muted">{action.status}{action.decided_by ? ` · ${action.decided_by}` : ''}</span>
      </header>
      <p className="mt-1 text-muted">{action.reason}</p>

      {message && (
        <blockquote className="mt-3 rounded-md bg-sunken p-3">
          <p className="font-semibold">{message.subject}</p>
          <p className="mt-1 whitespace-pre-wrap">{message.body}</p>
          <p className="mt-2 text-[12px] text-muted">Drafted from a template. Messages are shown here and never emailed.</p>
        </blockquote>
      )}

      {proposed && isMoney && preflight && (
        <div className="mt-3 grid gap-3 lg:grid-cols-2">
          <div>
            <h4 className="h-section mb-1.5">Safety checks</h4>
            <ul className="space-y-1.5">
              {preflight.checks.map((c) => (
                <li key={c.id} className="flex gap-2">
                  <svg width="16" height="16" viewBox="0 0 16 16" className="mt-0.5 shrink-0" fill="none" stroke={c.ok ? 'var(--pine)' : 'var(--brick)'} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-label={c.ok ? 'Passed' : 'Failed'}>
                    {c.ok ? <path d="M3 8.5l3.2 3L13 4.8" /> : <path d="M4.5 4.5l7 7M11.5 4.5l-7 7" />}
                  </svg>
                  <span><span className="font-semibold">{c.label}.</span> <span className="text-muted">{c.detail}</span></span>
                </li>
              ))}
            </ul>
            <p className="mt-2 text-[12px] text-muted">{preflight.note}</p>
          </div>
          {preflight.preview && (
            <div>
              <h4 className="h-section mb-1.5">Exact request that will be sent</h4>
              <dl className="rounded-md bg-sunken p-3 font-mono text-[12px] leading-6">
                <div><dt className="inline text-muted">{preflight.preview.method} </dt><dd className="inline break-all">{preflight.preview.endpoint}</dd></div>
                {preflight.preview.sender_batch_id && <div><dt className="inline text-muted">sender_batch_id </dt><dd className="inline">{preflight.preview.sender_batch_id}</dd></div>}
                {preflight.preview.request_id && <div><dt className="inline text-muted">PayPal-Request-Id </dt><dd className="inline">{preflight.preview.request_id}</dd></div>}
                {preflight.preview.receiver && <div><dt className="inline text-muted">receiver </dt><dd className="inline">{preflight.preview.receiver}</dd></div>}
                {preflight.preview.amount && <div><dt className="inline text-muted">amount </dt><dd className="inline">{money(preflight.preview.amount, preflight.preview.currency ?? 'USD')}</dd></div>}
              </dl>
            </div>
          )}
        </div>
      )}

      {action.status === 'failed' && action.result?.error ? (
        <p role="status" className="mt-3 rounded-md border border-brick/40 bg-brick/10 px-3 py-2 text-brick">
          Refused safely: {String(action.result.error)}
        </p>
      ) : null}

      {proposed && (
        <footer className="mt-4 flex flex-wrap items-center gap-2">
          <button
            className="btn btn-primary"
            disabled={!ready || !approver.trim() || approve.isPending}
            onClick={() => {
              if (!isMoney || window.confirm(`Approve this action as ${approver}?\n\nPayPal will be read again first, and nothing runs if the fresh state disagrees.`)) approve.mutate()
            }}
          >
            {approve.isPending ? 'Working…' : action.kind === 'notify' ? 'Mark as sent' : action.kind === 'escalate' || action.kind === 'flag' ? 'Acknowledge' : 'Approve'}
          </button>
          <button className="btn btn-quiet" disabled={!approver.trim() || reject.isPending} onClick={() => reject.mutate()}>Reject</button>
          <label className="ml-auto flex items-center gap-1.5 text-[12px] text-muted">
            Snooze
            <select className="field !w-auto !py-1" value="" aria-label="Snooze this decision" disabled={snooze.isPending} onChange={(e) => e.target.value && snooze.mutate(Number(e.target.value))}>
              <option value="">Remind me in…</option>
              <option value="1">1 day</option>
              <option value="3">3 days</option>
              <option value="7">7 days</option>
            </select>
          </label>
          {!approver.trim() && <span className="basis-full text-[12px] text-muted">Enter your name in Acting as (left menu) to act.</span>}
          {isMoney && !ready && approver.trim() && <span className="text-[12px] text-brick">A safety check is failing, so approval is disabled.</span>}
        </footer>
      )}
    </article>
  )
}
