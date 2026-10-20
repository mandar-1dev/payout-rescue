import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'
import type { Action } from '../lib/types'
import { ActionCard } from '../components/ActionCard'
import { Empty, ErrorNote, Skeleton } from '../components/ui'
import { money } from '../lib/format'

export function Approvals({ approver }: { approver: string }) {
  const q = useQuery({ queryKey: ['actions', 'proposed'], queryFn: () => api<Action[]>('/api/actions?status=proposed') })
  if (q.isLoading) return <Skeleton className="h-64" />
  if (q.error) return <ErrorNote error={q.error} />
  const list = q.data ?? []
  const order = { reissue: 0, cancel: 1, escalate: 2, notify: 3, flag: 4 } as Record<string, number>
  const sorted = [...list].sort((a, b) => (order[a.kind] ?? 9) - (order[b.kind] ?? 9))
  const moneyMoving = sorted.filter((a) => a.kind === 'cancel' || a.kind === 'reissue')
  const rest = sorted.filter((a) => a.kind !== 'cancel' && a.kind !== 'reissue')

  if (list.length === 0) return <Empty title="Nothing waiting for a decision">New proposals appear here when a payout fails, sits unclaimed, or a recipient replies.</Empty>

  const heading = (a: Action) => (
    <span>
      <Link className="text-marine hover:underline" to={`/items/${a.item_id}`}>{a.item?.recipient_name ?? 'Payout'}</Link>
      {a.item ? <span className="font-normal text-muted"> · {money(a.item.amount, a.item.currency)}</span> : null}
    </span>
  )

  return (
    <div className="space-y-6">
      {moneyMoving.length > 0 && (
        <section className="space-y-3">
          <h2 className="h-section">These move money · {moneyMoving.length}</h2>
          {moneyMoving.map((a) => <ActionCard key={a.id} action={a} preflight={a.preflight} approver={approver} heading={a.kind === 'reissue' ? 'Send one replacement' : 'Cancel unclaimed payout'} />)}
          <p className="text-[12px] text-muted">Each decision lists the exact request. Open the payout for the full conversation and history.</p>
        </section>
      )}
      {rest.length > 0 && (
        <section className="space-y-3">
          <h2 className="h-section">Everything else · {rest.length}</h2>
          {rest.map((a) => (
            <div key={a.id}>
              <p className="mb-1 font-semibold">{heading(a)}</p>
              <ActionCard action={a} approver={approver} />
            </div>
          ))}
        </section>
      )}
    </div>
  )
}
