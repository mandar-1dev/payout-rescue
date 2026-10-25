import { useMemo, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { AgGridReact } from 'ag-grid-react'
import { AllCommunityModule, ModuleRegistry, themeQuartz, type ColDef, type GridApi, type GridState, type ICellRendererParams } from 'ag-grid-community'
import { api, listConflicts, seedConflicts } from '../lib/api'
import type { Conflict, Item, Mode, Summary } from '../lib/types'
import { conflictInfo } from './Conflicts'
import { explain, money, plural } from '../lib/format'
import { Empty, ErrorNote, LimboRail, ModeBadge, Skeleton, StatusChip, useToast } from '../components/ui'

ModuleRegistry.registerModules([AllCommunityModule])

const gridTheme = themeQuartz.withParams({
  fontFamily: 'Public Sans Variable, system-ui, sans-serif',
  fontSize: 13,
  backgroundColor: 'var(--surface)',
  foregroundColor: 'var(--ink)',
  headerBackgroundColor: 'var(--sunken)',
  headerTextColor: 'var(--muted)',
  headerFontWeight: 600,
  borderColor: 'var(--line)',
  rowHoverColor: 'color-mix(in srgb, var(--marine) 8%, transparent)',
  accentColor: 'var(--marine)',
  wrapperBorderRadius: 8,
  rowBorder: { color: 'var(--line)' },
  spacing: 7,
})

const STATE_KEY = 'payoutrescue.grid'
const STATUS_ORDER = ['UNCLAIMED', 'FAILED', 'ONHOLD', 'BLOCKED', 'RETURNED', 'CANCELED', 'SUCCESS']

export function Dashboard() {
  const nav = useNavigate()
  const toast = useToast()
  const gridApi = useRef<GridApi<Item> | null>(null)
  const items = useQuery({ queryKey: ['items'], queryFn: () => api<Item[]>('/api/items') })
  const summary = useQuery({ queryKey: ['limbo'], queryFn: () => api<Summary>('/api/limbo') })
  const openConflicts = useQuery({ queryKey: ['conflicts', 'open'], queryFn: () => listConflicts('open') })
  const conflictsByItem = useMemo(() => {
    const m = new Map<string, Conflict[]>()
    for (const c of openConflicts.data ?? []) m.set(c.item_id, [...(m.get(c.item_id) ?? []), c])
    return m
  }, [openConflicts.data])
  const [statuses, setStatuses] = useState<Set<string>>(new Set())
  const [mode, setMode] = useState<'' | Mode>('')
  const [q, setQ] = useState('')

  const rows = useMemo(() => {
    const list = items.data ?? []
    return list.filter((i) => (statuses.size === 0 || statuses.has(i.status)) && (!mode || i.mode === mode))
  }, [items.data, statuses, mode])

  const cols = useMemo<ColDef<Item>[]>(
    () => [
      {
        headerName: 'Recipient', field: 'recipient_name', flex: 1.6, minWidth: 190,
        getQuickFilterText: (p) => `${p.data?.recipient_name ?? ''} ${p.data?.recipient_email ?? ''} ${p.data?.purpose ?? ''}`,
        cellRenderer: (p: ICellRendererParams<Item>) => (
          <div className="flex h-full flex-col justify-center leading-tight">
            <span className="truncate font-semibold">{p.data?.recipient_name}</span>
            <span className="truncate text-[12px] text-muted">{p.data?.corrected_email ? `${p.data.corrected_email} (corrected)` : p.data?.recipient_email}</span>
          </div>
        ),
      },
      {
        headerName: 'Amount', flex: 0.8, minWidth: 100, type: 'rightAligned',
        valueGetter: (p) => (p.data ? parseFloat(p.data.amount) : 0),
        cellRenderer: (p: ICellRendererParams<Item>) => <span className="num font-semibold">{p.data ? money(p.data.amount, p.data.currency) : ''}</span>,
      },
      {
        headerName: 'Status', flex: 1, minWidth: 130, field: 'status',
        cellRenderer: (p: ICellRendererParams<Item>) => (
          <div className="flex h-full items-center gap-2">
            {p.data && <StatusChip status={p.data.status} />}
            {p.data && p.data.attempt > 1 && <span className="text-[11px] text-muted">try {p.data.attempt}</span>}
          </div>
        ),
      },
      {
        headerName: 'What happened', flex: 2.2, minWidth: 170, wrapText: true, autoHeight: false,
        valueGetter: (p) => (p.data ? explain(p.data.status, p.data.error_code) : ''),
        tooltipValueGetter: (p) => (p.data ? `${p.data.error_code || p.data.status}` : ''),
        cellClass: 'text-muted !leading-tight flex items-center',
        cellStyle: { whiteSpace: 'normal' },
      },
      {
        headerName: 'Time before return', flex: 1.2, minWidth: 170,
        valueGetter: (p) => (p.data?.status === 'UNCLAIMED' ? p.data.days_left : 999),
        cellRenderer: (p: ICellRendererParams<Item>) =>
          p.data?.status === 'UNCLAIMED' ? (
            <div className="flex h-full items-center"><LimboRail age={p.data.age_days} /></div>
          ) : (
            <span className="whitespace-normal text-[12px] leading-tight text-muted">Not in the 30-day window</span>
          ),
      },
      {
        headerName: 'Conflict', flex: 1.1, minWidth: 130, valueGetter: (p) => (p.data ? (conflictsByItem.get(p.data.id)?.length ?? 0) : 0),
        cellRenderer: (p: ICellRendererParams<Item>) => {
          const cs = p.data ? conflictsByItem.get(p.data.id) : undefined
          if (!cs?.length) return <span className="text-muted">None</span>
          return <div className="flex h-full items-center"><span className="chip" style={{ ['--c' as string]: 'var(--brick)' }} title={cs.map((c) => conflictInfo[c.kind]?.title ?? c.kind).join(', ')}>{cs.length === 1 ? (conflictInfo[cs[0].kind]?.title ?? cs[0].kind) : `${cs.length} conflicts`}</span></div>
        },
      },
      { headerName: 'Source', flex: 0.9, minWidth: 110, field: 'mode', cellRenderer: (p: ICellRendererParams<Item>) => (p.data ? <div className="flex h-full items-center"><ModeBadge mode={p.data.mode} /></div> : null) },
      {
        headerName: 'Waiting on you', flex: 1, minWidth: 120, field: 'open_actions',
        cellRenderer: (p: ICellRendererParams<Item>) =>
          p.data?.open_actions ? <span className="font-semibold text-marine">{plural(p.data.open_actions, 'action')}</span> : <span className="text-muted">Nothing</span>,
      },
      {
        headerName: '', flex: 0.6, minWidth: 80, sortable: false, filter: false,
        cellRenderer: (p: ICellRendererParams<Item>) => (
          <Link className="font-semibold text-marine underline-offset-2 hover:underline" to={`/items/${p.data?.id}`} aria-label={`Review ${p.data?.recipient_name}`}>
            Review
          </Link>
        ),
      },
    ],
    [conflictsByItem],
  )

  const initialState = useMemo<GridState | undefined>(() => {
    try {
      const raw = localStorage.getItem(STATE_KEY)
      return raw ? (JSON.parse(raw) as GridState) : undefined
    } catch {
      return undefined
    }
  }, [])

  const s = summary.data
  const counts = items.data?.reduce<Record<string, number>>((m, i) => ((m[i.status] = (m[i.status] ?? 0) + 1), m), {}) ?? {}

  return (
    <div className="space-y-5">
      <section aria-live="polite" className="panel rise p-5">
        {summary.isLoading ? (
          <Skeleton className="h-16" />
        ) : summary.error ? (
          <ErrorNote error={summary.error} />
        ) : s ? (
          <p className="max-w-4xl text-[17px] leading-8">
            {s.in_limbo && s.in_limbo.length > 0 ? (
              <>
                <b className="num">{s.in_limbo.map((c) => money(c.amount, c.currency)).join(' + ')}</b> is in limbo across{' '}
                <b>{(s.by_status.UNCLAIMED ?? 0) + (s.by_status.ONHOLD ?? 0) + (s.by_status.BLOCKED ?? 0) + (s.by_status.PENDING ?? 0)}</b> payouts.{' '}
              </>
            ) : (
              <>No money is waiting in limbo. </>
            )}
            <Link to="/approvals" className="font-semibold text-marine underline decoration-marine/40 underline-offset-4">
              {plural(s.needing_action, 'decision')} wait for you.
            </Link>{' '}
            {s.next_return_days !== null ? (
              <>The next automatic return by PayPal is in <b>{plural(s.next_return_days, 'day')}</b>. </>
            ) : null}
            {(s.open_conflicts ?? 0) > 0 && (
              <Link to="/conflicts" className="font-semibold text-brick underline decoration-brick/40 underline-offset-4">{plural(s.open_conflicts ?? 0, 'conflict')} need a person. </Link>
            )}
            <span className="text-muted">{plural(s.duplicates_prevented, 'duplicate payment')} prevented so far.</span>
          </p>
        ) : null}
      </section>

      <section className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <div role="group" aria-label="Filter by status" className="flex flex-wrap gap-1.5">
            {STATUS_ORDER.filter((st) => counts[st]).map((st) => {
              const on = statuses.has(st)
              return (
                <button
                  key={st}
                  aria-pressed={on}
                  onClick={() =>
                    setStatuses((prev) => {
                      const n = new Set(prev)
                      if (on) n.delete(st)
                      else n.add(st)
                      return n
                    })
                  }
                  className={`rounded-full border px-2.5 py-0.5 text-[12px] font-semibold ${on ? 'border-marine bg-marine text-marine-ink' : 'border-line bg-surface hover:bg-sunken'}`}
                >
                  {st.charAt(0) + st.slice(1).toLowerCase()} · {counts[st]}
                </button>
              )
            })}
          </div>
          <div className="ml-auto flex flex-wrap items-center gap-2">
            <select aria-label="Filter by source" className="field !w-auto" value={mode} onChange={(e) => setMode(e.target.value as '' | Mode)}>
              <option value="">All sources</option>
              <option value="LIVE_SANDBOX">Live sandbox</option>
              <option value="SIMULATED">Simulated</option>
              <option value="FAKE">Fake</option>
            </select>
            <input aria-label="Search recipients" className="field !w-56" placeholder="Search name or email" value={q} onChange={(e) => setQ(e.target.value)} />
            <button className="btn btn-quiet" onClick={() => gridApi.current?.exportDataAsCsv({ fileName: 'payoutrescue-items.csv' })}>
              Export CSV
            </button>
          </div>
        </div>

        {items.isLoading ? (
          <Skeleton className="h-64" />
        ) : items.error ? (
          <ErrorNote error={items.error} />
        ) : (items.data?.length ?? 0) === 0 ? (
          <Empty title="No payouts yet">
            Create a batch, or load the demo data to see stuck, failed and unclaimed payouts.
            <div className="mt-3 flex justify-center gap-2">
              <button className="btn btn-primary" onClick={() => void api('/api/demo/seed', { method: 'POST' }).then(() => toast('Demo data loaded')).catch((e: Error) => toast(e.message, 'error'))}>Load demo data</button>
              <button className="btn btn-quiet" onClick={() => void seedConflicts().then(() => toast('Conflict pack loaded')).catch((e: Error) => toast(e.message, 'error'))}>Load conflict pack</button>
              <Link className="btn btn-quiet" to="/batches">Create a batch</Link>
            </div>
          </Empty>
        ) : (
          <div className="rise" style={{ width: '100%' }}>
            <AgGridReact<Item>
              theme={gridTheme}
              rowData={rows}
              columnDefs={cols}
              defaultColDef={{ sortable: true, resizable: true, filter: false, suppressMovable: false }}
              quickFilterText={q}
              domLayout="autoHeight"
              rowHeight={58}
              headerHeight={40}
              getRowId={(p) => p.data.id}
              initialState={initialState}
              onStateUpdated={(e) => localStorage.setItem(STATE_KEY, JSON.stringify(e.api.getState()))}
              onGridReady={(e) => { gridApi.current = e.api }}
              onRowClicked={(e) => e.data && nav(`/items/${e.data.id}`)}
              rowClass="cursor-pointer"
              suppressCellFocus
              tooltipShowDelay={400}
              overlayNoRowsTemplate="No payouts match these filters"
            />
          </div>
        )}
      </section>

      {s && s.buckets.some((b) => b.count > 0) && (
        <section className="panel p-5">
          <h2 className="h-section mb-3">How long money has been waiting</h2>
          <div className="space-y-2">
            {s.buckets.map((b) => {
              const max = Math.max(...s.buckets.map((x) => x.count), 1)
              return (
                <div key={b.label} className="grid grid-cols-[88px_1fr_160px] items-center gap-3">
                  <span className="text-muted num">{b.label} days</span>
                  <div className="h-3 rounded-full" style={{ background: 'var(--sunken)' }}>
                    <div className="h-3 rounded-full" style={{ width: `${(b.count / max) * 100}%`, background: b.label === '22-27' ? 'var(--signal)' : b.label === '28-30' ? 'var(--brick)' : 'var(--marine)' }} />
                  </div>
                  <span className="num text-right text-muted">{b.count ? `${plural(b.count, 'payout')} · ${b.amount}` : 'none'}</span>
                </div>
              )
            })}
          </div>
        </section>
      )}
    </div>
  )
}
