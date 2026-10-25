import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, bulkAcknowledge } from '../lib/api'
import type { Action } from '../lib/types'
import { ActionCard } from '../components/ActionCard'
import { Empty, ErrorNote, Skeleton, useToast } from '../components/ui'
import { money } from '../lib/format'

const isMoney = (a: Action) => a.kind === 'cancel' || a.kind === 'reissue'

export function Approvals({ approver }: { approver: string }) {
  const qc = useQueryClient()
  const toast = useToast()
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const q = useQuery({ queryKey: ['actions', 'proposed'], queryFn: () => api<Action[]>('/api/actions?status=proposed') })

  const bulk = useMutation({
    mutationFn: () => bulkAcknowledge([...picked], approver),
    onSuccess: (r) => { toast(`Acknowledged ${r.acknowledged}.`); setPicked(new Set()); void qc.invalidateQueries() },
    onError: (e: Error) => toast(e.message, 'error'),
  })

  if (q.isLoading) return <Skeleton className="h-64" />
  if (q.error) return <ErrorNote error={q.error} />
  const list = q.data ?? []
  const order: Record<string, number> = { reissue: 0, cancel: 1, escalate: 2, notify: 3, flag: 4 }
  const sorted = [...list].sort((a, b) => (order[a.kind] ?? 9) - (order[b.kind] ?? 9))
  const moneyMoving = sorted.filter(isMoney)
  const rest = sorted.filter((a) => !isMoney(a))

  if (list.length === 0) return <Empty title="Nothing waiting for a decision">New proposals appear here when a payout fails, sits unclaimed, or a recipient replies. Snoozed decisions come back later.</Empty>

  const heading = (a: Action) => (
    <span>
      <Link className="text-marine hover:underline" to={`/items/${a.item_id}`}>{a.item?.recipient_name ?? 'Payout'}</Link>
      {a.item ? <span className="font-normal text-muted"> · {money(a.item.amount, a.item.currency)}</span> : null}
    </span>
  )
  const toggle = (id: string) => setPicked((p) => { const n = new Set(p); if (n.has(id)) n.delete(id); else n.add(id); return n })
  const allPicked = rest.length > 0 && rest.every((a) => picked.has(a.id))

  return (
    <div className="space-y-6">
      {moneyMoving.length > 0 && (
        <section className="space-y-3">
          <h2 className="h-section">These move money · {moneyMoving.length}</h2>
          <p className="rounded-md border border-line bg-surface px-3 py-2 text-[13px] text-muted">Decisions that move money have no checkbox on purpose. Each is approved one at a time, with your name, after its safety checks.</p>
          {moneyMoving.map((a) => <ActionCard key={a.id} action={a} preflight={a.preflight} approver={approver} heading={a.kind === 'reissue' ? 'Send one replacement' : 'Cancel unclaimed payout'} />)}
        </section>
      )}
      {rest.length > 0 && (
        <section className="space-y-3">
          <div className="flex flex-wrap items-center gap-3">
            <h2 className="h-section">Messages and notes · {rest.length}</h2>
            <label className="flex items-center gap-1.5 text-[13px]"><input type="checkbox" checked={allPicked} onChange={() => setPicked(allPicked ? new Set() : new Set(rest.map((a) => a.id)))} /> Select all</label>
            <button className="btn btn-primary ml-auto" disabled={picked.size === 0 || !approver.trim() || bulk.isPending} onClick={() => bulk.mutate()}>
              {bulk.isPending ? 'Working…' : `Acknowledge selected (${picked.size})`}
            </button>
          </div>
          {!approver.trim() && <p className="text-[12px] text-muted">Enter your name in Acting as (left menu) to acknowledge.</p>}
          {rest.map((a) => (
            <div key={a.id} className="grid grid-cols-[auto_1fr] gap-3">
              <input type="checkbox" className="mt-2" aria-label={`Select ${a.item?.recipient_name ?? 'action'}`} checked={picked.has(a.id)} onChange={() => toggle(a.id)} />
              <div>
                <p className="mb-1 font-semibold">{heading(a)}</p>
                <ActionCard action={a} approver={approver} />
              </div>
            </div>
          ))}
        </section>
      )}
    </div>
  )
}
