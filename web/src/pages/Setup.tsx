import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import type { Session, Summary } from '../lib/types'
import { ErrorNote, Skeleton, useToast } from '../components/ui'

function Row({ ok, name, detail }: { ok: boolean | null; name: string; detail: string }) {
  const color = ok === null ? 'var(--muted)' : ok ? 'var(--pine)' : 'var(--signal)'
  return (
    <li className="flex items-start gap-3 border-t border-line py-3 first:border-0">
      <span className="chip mt-0.5 shrink-0" style={{ ['--c' as string]: color }}>{ok === null ? 'Info' : ok ? 'Ready' : 'Not set'}</span>
      <div><p className="font-semibold">{name}</p><p className="break-all text-muted">{detail}</p></div>
    </li>
  )
}

export function Setup() {
  const qc = useQueryClient()
  const toast = useToast()
  const s = useQuery({ queryKey: ['session'], queryFn: () => api<Session>('/api/session') })
  const sum = useQuery({ queryKey: ['limbo'], queryFn: () => api<Summary>('/api/limbo') })
  const advance = useMutation({
    mutationFn: (days: number) => api('/api/admin/advance', { method: 'POST', json: { days } }),
    onSuccess: (_r, days) => { toast(`Moved fake time forward ${days} days.`); void qc.invalidateQueries() },
    onError: (e: Error) => toast(e.message, 'error'),
  })
  const recon = useMutation({
    mutationFn: () => api<{ items: number; batches: number; errors: string[] | null }>('/api/admin/reconcile', { method: 'POST' }),
    onSuccess: (r) => { toast(`Reconciled ${r.batches} batches and ${r.items} items.${r.errors?.length ? ` ${r.errors.length} errors.` : ''}`, r.errors?.length ? 'warn' : 'ok'); void qc.invalidateQueries() },
    onError: (e: Error) => toast(e.message, 'error'),
  })
  if (s.isLoading) return <Skeleton className="h-64" />
  if (s.error || !s.data) return <ErrorNote error={s.error ?? new Error('No data')} />
  const d = s.data
  return (
    <div className="grid gap-5 lg:grid-cols-2">
      <section className="panel p-5">
        <h2 className="mb-1 text-lg font-bold">Connections</h2>
        <ul>
          <Row ok name="Database" detail="Reachable; migrations applied." />
          <Row ok={d.live_configured} name="PayPal sandbox" detail={d.live_configured ? 'Credentials set. Live sandbox and simulated batches are available.' : 'PAYPAL_CLIENT_ID and PAYPAL_CLIENT_SECRET are not set, so only the fake PayPal runs.'} />
          <Row ok={d.webhook_id_set} name="Webhook signature verification" detail={d.webhook_bypass ? 'Bypass is ON: signatures are not verified. Use only for sandbox simulator events.' : d.webhook_id_set ? 'PAYPAL_WEBHOOK_ID is set. Real events are verified through PayPal.' : 'PAYPAL_WEBHOOK_ID is not set, so real webhooks will be refused.'} />
          <Row ok={d.webhook_url ? true : false} name="Webhook URL" detail={d.webhook_url || 'Set PUBLIC_BASE_URL so the exact URL to register is shown here.'} />
          <Row ok={d.llm !== 'none'} name="Language model" detail={d.llm === 'none' ? 'None configured. Templates and deterministic reply reading are used.' : d.llm} />
          <Row ok={null} name="Reconciler" detail={`Runs every ${d.reconcile_seconds} seconds and recovers missed webhooks, lost responses and unfinished approvals.`} />
          <Row ok={sum.data ? sum.data.audit_intact : null} name="Audit chain" detail={sum.data ? `${sum.data.audit_entries} entries; ${sum.data.audit_intact ? 'intact' : 'BROKEN'}` : 'Checking…'} />
        </ul>
      </section>

      <div className="space-y-5">
        <section className="panel space-y-3 p-5">
          <h2 className="text-lg font-bold">Time and reconciliation</h2>
          <p className="text-muted">Move time forward to see day 20, 27 and 30 behaviour. This only affects items created in Fake mode. PayPal’s real clock cannot be moved.</p>
          <div className="flex flex-wrap gap-2">
            {[1, 7, 20, 27, 30].map((n) => <button key={n} className="btn btn-quiet" disabled={!d.fake_enabled || advance.isPending} onClick={() => advance.mutate(n)}>+{n} {n === 1 ? 'day' : 'days'}</button>)}
            <button className="btn btn-primary" disabled={recon.isPending} onClick={() => recon.mutate()}>Reconcile now</button>
          </div>
        </section>
        <section className="panel p-5">
          <h2 className="mb-2 text-lg font-bold">Register the PayPal webhook</h2>
          <ol className="list-decimal space-y-1.5 pl-5 text-muted">
            <li>Open the PayPal developer dashboard, then your sandbox app.</li>
            <li>Add a webhook that points to the URL shown on the left.</li>
            <li>Subscribe to the payout item and payout batch events.</li>
            <li>Copy the webhook ID into <span className="font-mono text-ink">PAYPAL_WEBHOOK_ID</span> and redeploy.</li>
            <li>Create a live sandbox batch and check that the event arrives in the audit log.</li>
          </ol>
        </section>
        <section className="panel p-5">
          <h2 className="mb-2 text-lg font-bold">PayPal test values in use</h2>
          <ul className="space-y-1 text-muted">
            {Object.entries(d.sim_tags).map(([tag, what]) => <li key={tag}><span className="font-mono text-ink">{tag}</span> simulates {what}</li>)}
          </ul>
        </section>
      </div>
    </div>
  )
}
