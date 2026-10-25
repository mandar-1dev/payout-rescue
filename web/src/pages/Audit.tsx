import { useQuery } from '@tanstack/react-query'
import { api, download } from '../lib/api'
import type { AuditEntry } from '../lib/types'
import { ErrorNote, Skeleton, useToast } from '../components/ui'
import { when } from '../lib/format'

export function Audit() {
  const toast = useToast()
  const log = useQuery({ queryKey: ['audit'], queryFn: () => api<AuditEntry[]>('/api/audit?limit=300') })
  const ver = useQuery({ queryKey: ['audit-verify'], queryFn: () => api<{ entries: number; intact: boolean; first_broken_id: number }>('/api/audit/verify') })
  return (
    <div className="space-y-4">
      <section className="panel flex flex-wrap items-center gap-3 p-4">
        <h2 className="text-lg font-bold">Audit log</h2>
        <button className="btn btn-quiet ml-auto" onClick={() => download('/api/audit/export.csv', 'audit_log.csv').catch((e: Error) => toast(e.message, 'error'))}>Export CSV</button>
        {ver.data && (
          <span className="chip" style={{ ['--c' as string]: ver.data.intact ? 'var(--pine)' : 'var(--brick)' }}>
            {ver.data.intact ? `Chain intact · ${ver.data.entries} entries` : `Chain broken at entry ${ver.data.first_broken_id}`}
          </span>
        )}
        <p className="basis-full text-muted">Every entry stores the hash of the one before it, and the database refuses edits and deletes. Approvals record the name of the person who approved.</p>
      </section>
      {log.isLoading ? <Skeleton className="h-64" /> : log.error ? <ErrorNote error={log.error} /> : (
        <div className="panel overflow-x-auto">
          <table className="w-full min-w-[720px] text-left">
            <thead><tr className="text-[12px] text-muted"><th className="px-4 py-2 font-semibold">#</th><th className="px-2 py-2 font-semibold">When</th><th className="px-2 py-2 font-semibold">Who</th><th className="px-2 py-2 font-semibold">Event</th><th className="px-2 py-2 font-semibold">Details</th><th className="px-4 py-2 font-semibold">Hash</th></tr></thead>
            <tbody>
              {log.data?.map((e) => (
                <tr key={e.id} className="border-t border-line align-top">
                  <td className="num px-4 py-2 text-muted">{e.id}</td>
                  <td className="num px-2 py-2 whitespace-nowrap">{when(e.ts)}</td>
                  <td className="px-2 py-2">{e.actor}</td>
                  <td className="px-2 py-2 font-semibold">{e.event.replace(/_/g, " ")}</td>
                  <td className="max-w-xs px-2 py-2 text-muted"><span className="line-clamp-2 break-all text-[12px]">{JSON.stringify(e.details)}</span></td>
                  <td className="px-4 py-2 font-mono text-[11px] text-muted">{e.hash.slice(0, 10)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
