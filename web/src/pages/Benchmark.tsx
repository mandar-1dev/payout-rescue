import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import type { BenchLatest, ScenarioResult } from '../lib/types'
import { ErrorNote, ModeBadge, Skeleton, useToast } from '../components/ui'
import { when } from '../lib/format'

function Metric({ name, how, naive, pr, naiveNote }: { name: string; how: string; naive: number | string; pr: number | string; naiveNote?: string }) {
  return (
    <tr className="border-t border-line align-top">
      <td className="py-3 pr-4"><p className="font-semibold">{name}</p><p className="text-[12px] text-muted">{how}</p></td>
      <td className="num px-4 py-3 text-lg font-bold text-brick">{naive}{naiveNote && <span className="block text-[11px] font-normal text-muted">{naiveNote}</span>}</td>
      <td className="num px-4 py-3 text-lg font-bold text-pine">{pr}</td>
    </tr>
  )
}

export function Benchmark() {
  const qc = useQueryClient()
  const toast = useToast()
  const q = useQuery({ queryKey: ['bench'], queryFn: () => api<BenchLatest>('/api/bench/latest'), refetchInterval: (query) => (query.state.data?.running ? 2000 : false) })
  const run = useMutation({
    mutationFn: () => api('/api/bench/run', { method: 'POST' }),
    onSuccess: () => { toast('Benchmark started. It takes a few seconds.'); void qc.invalidateQueries({ queryKey: ['bench'] }) },
    onError: (e: Error) => toast(e.message, 'error'),
  })

  const r = q.data?.result
  const groups = new Map<string, ScenarioResult[]>()
  r?.scenarios.forEach((s) => groups.set(s.group, [...(groups.get(s.group) ?? []), s]))

  return (
    <div className="space-y-5">
      <section className="panel p-5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="max-w-2xl">
            <h2 className="text-lg font-bold">Naive retry script versus PayoutRescue</h2>
            <p className="mt-1 text-muted">
              The same scripted situations run against a typical “retry anything that is not paid” script and against PayoutRescue. These are scripted scenarios, not real-world measurements.
            </p>
          </div>
          <button className="btn btn-primary" disabled={run.isPending || q.data?.running} onClick={() => run.mutate()}>
            {q.data?.running ? 'Running…' : r ? 'Run again' : 'Run the benchmark'}
          </button>
        </div>
      </section>

      {q.isLoading ? <Skeleton className="h-48" /> : q.error ? <ErrorNote error={q.error} /> : !r ? (
        <div className="rounded-lg border border-dashed border-line p-8 text-center text-muted">No results yet. Run the benchmark to generate them.</div>
      ) : (
        <>
          <section className="panel overflow-x-auto p-5">
            <p className="mb-3 text-muted">
              <b>{r.summary.passed}</b> of <b>{r.summary.scenarios}</b> scenarios behaved as required · {r.summary.comparison_scenarios} compared head to head, {r.summary.payoutrescue_only_scenarios} only apply to PayoutRescue · run {when(r.run_at)} in {r.seconds.toFixed(1)}s
            </p>
            <table className="w-full min-w-[560px] text-left">
              <thead><tr className="text-[12px] text-muted"><th className="pb-2 font-semibold">Measure</th><th className="px-4 pb-2 font-semibold">Retry script</th><th className="px-4 pb-2 font-semibold">PayoutRescue</th></tr></thead>
              <tbody>
                <Metric name="Recipients paid twice" how="Counted from the fake PayPal ledger" naive={r.summary.naive_duplicate_payments} pr={r.summary.payoutrescue_duplicate_payments} />
                <Metric name="Pointless retries of errors that cannot succeed" how="Re-sending after country, risk, currency or duplicate errors, or from a held item" naive={r.summary.naive_unsafe_retries} pr={r.summary.payoutrescue_unsafe_retries} />
                <Metric name="Returns nobody was told about" how="Items PayPal returned with no reminder or cancel proposed" naive={r.summary.naive_unnoticed_returns} pr={r.summary.payoutrescue_unnoticed_returns} />
                <Metric name="Operator steps" how={`Retry script: an ASSUMED ${r.summary.assumed_manual_steps_per_exception} manual steps per problem item. PayoutRescue: approvals and corrections actually performed.`} naive={r.summary.naive_manual_steps_assumed} pr={r.summary.payoutrescue_operator_steps} naiveNote="assumption, not measured" />
                <Metric name="Reply reading" how={`Built-in synthetic set of ${r.summary.extraction_total} replies, written alongside the code (not an independent test). ${r.summary.extraction_source}.`} naive="n/a" pr={`${r.summary.extraction_correct}/${r.summary.extraction_total}`} />
              </tbody>
            </table>
            <p className="mt-4 rounded-md bg-sunken p-3 text-[13px]">{r.disclaimer}</p>
            <p className="mt-2 text-[12px] text-muted">Scenarios by source: {Object.entries(r.summary.label_counts).map(([k, v]) => `${k.replace('_', ' ').toLowerCase()} ${v}`).join(', ')}</p>
          </section>

          <section className="space-y-4">
            {[...groups.entries()].map(([group, list]) => (
              <div key={group} className="panel overflow-x-auto">
                <h3 className="h-section border-b border-line px-4 py-2.5">{group}</h3>
                <table className="w-full min-w-[640px] text-left">
                  <tbody>
                    {list.map((s) => (
                      <tr key={s.id} className="border-t border-line first:border-0">
                        <td className="w-14 px-4 py-2 text-muted num">{s.id}</td>
                        <td className="py-2">{s.name}{s.note && <span className="block text-[12px] text-muted">{s.note}</span>}</td>
                        <td className="px-2 py-2"><ModeBadge mode={s.label} /></td>
                        <td className="num px-2 py-2 text-[12px] text-muted">{s.naive.skipped ? 'no comparison' : `script: ${s.naive.duplicate_payments} double, ${s.naive.unsafe_retries} bad retries`}</td>
                        <td className="px-4 py-2 text-right font-semibold" style={{ color: s.pass ? 'var(--pine)' : 'var(--brick)' }}>{s.pass ? 'Passed' : 'Failed'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ))}
          </section>
        </>
      )}
    </div>
  )
}
