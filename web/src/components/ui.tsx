import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react'
import type { Mode, Status } from '../lib/types'

/* ---------------------------------------------------------------- status chip */

const statusStyle: Record<string, { label: string; color: string; icon: ReactNode }> = {
  SUCCESS: { label: 'Paid', color: 'var(--pine)', icon: <path d="M3 8.5l3.2 3L13 4.8" /> },
  UNCLAIMED: { label: 'Unclaimed', color: 'var(--signal)', icon: <><circle cx="8" cy="8" r="5.2" /><path d="M8 5v3.2l2 1.3" /></> },
  FAILED: { label: 'Failed', color: 'var(--brick)', icon: <path d="M4.5 4.5l7 7M11.5 4.5l-7 7" /> },
  ONHOLD: { label: 'On hold', color: 'var(--iris)', icon: <path d="M6 4.5v7M10 4.5v7" /> },
  BLOCKED: { label: 'Blocked', color: 'var(--brick)', icon: <><circle cx="8" cy="8" r="5.2" /><path d="M4.4 11.6l7.2-7.2" /></> },
  RETURNED: { label: 'Returned', color: 'var(--muted)', icon: <path d="M6 4L3.5 6.5 6 9M3.5 6.5H10a2.5 2.5 0 010 5H8" /> },
  CANCELED: { label: 'Cancelled', color: 'var(--muted)', icon: <path d="M4 8h8" /> },
  PENDING: { label: 'Pending', color: 'var(--marine)', icon: <path d="M4 8h.01M8 8h.01M12 8h.01" /> },
  CREATED: { label: 'Sending', color: 'var(--marine)', icon: <path d="M4 8h.01M8 8h.01M12 8h.01" /> },
  REFUNDED: { label: 'Refunded', color: 'var(--muted)', icon: <path d="M6 4L3.5 6.5 6 9M3.5 6.5H10a2.5 2.5 0 010 5H8" /> },
  REVERSED: { label: 'Reversed', color: 'var(--muted)', icon: <path d="M6 4L3.5 6.5 6 9M3.5 6.5H10a2.5 2.5 0 010 5H8" /> },
  UNKNOWN: { label: 'Unknown', color: 'var(--brick)', icon: <path d="M6.2 6.2a1.9 1.9 0 113 1.5c-.7.5-1.2.8-1.2 1.6M8 11.6h.01" /> },
}

export function StatusChip({ status }: { status: Status | string }) {
  const s = statusStyle[status] ?? statusStyle.UNKNOWN
  return (
    <span className="chip" style={{ ['--c' as string]: s.color }}>
      <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        {s.icon}
      </svg>
      {s.label}
    </span>
  )
}

/* ---------------------------------------------------------------- mode badge */

const modeInfo: Record<Mode, { label: string; title: string; cls: string }> = {
  LIVE_SANDBOX: { label: 'Live sandbox', title: 'State came from PayPal’s sandbox', cls: 'border-marine text-marine' },
  SIMULATED: { label: 'Simulated', title: 'PayPal’s documented canned test-value responses', cls: 'border-iris text-iris border-dashed' },
  FAKE: { label: 'Fake', title: 'In-memory PayPal stand-in; not PayPal', cls: 'border-muted text-muted border-dotted' },
}

export function ModeBadge({ mode }: { mode: Mode }) {
  const m = modeInfo[mode]
  return (
    <span title={m.title} className={`inline-block rounded border px-1.5 py-px text-[11px] font-semibold ${m.cls}`}>
      {m.label}
    </span>
  )
}

/* ---------------------------------------------------------------- limbo rail */

/** The 30-day rail: PayPal returns unclaimed money at day 30. Day 20 and day 27 are where this system acts. */
export function LimboRail({ age, large = false }: { age: number; large?: boolean }) {
  const day = Math.max(0, Math.min(30, age))
  const pct = (d: number) => `${(d / 30) * 100}%`
  const left = 30 - day
  return (
    <div className={large ? 'w-full' : 'w-[150px]'} role="img" aria-label={`Day ${day} of 30. ${left} days left before PayPal returns the money.`}>
      <div className={`relative ${large ? 'h-3' : 'h-2'} overflow-hidden rounded-full`} style={{ background: 'var(--sunken)' }}>
        <div className="absolute inset-y-0" style={{ left: pct(20), width: pct(7), background: 'color-mix(in srgb, var(--signal) 28%, transparent)' }} />
        <div className="absolute inset-y-0 right-0" style={{ width: pct(3), background: 'color-mix(in srgb, var(--brick) 30%, transparent)' }} />
        <div className="absolute inset-y-0 left-0 rounded-full" style={{ width: pct(day), background: day >= 27 ? 'var(--brick)' : day >= 20 ? 'var(--signal)' : 'var(--marine)' }} />
      </div>
      {large ? (
        <div className="relative mt-1.5 h-4 text-[11px] text-muted num">
          {[[0, 'Day 0'], [20, 'Day 20 reminder'], [27, 'Day 27 decide'], [30, 'Day 30 returned']].map(([d, label]) => (
            <span key={String(d)} className="absolute whitespace-nowrap" style={{ left: pct(Number(d)), transform: Number(d) === 30 ? 'translateX(-100%)' : Number(d) === 0 ? 'none' : 'translateX(-50%)' }}>
              {label}
            </span>
          ))}
        </div>
      ) : (
        <div className="mt-0.5 text-[11px] text-muted num">{left > 0 ? `${left} days left` : 'returns today'}</div>
      )}
    </div>
  )
}

/* ---------------------------------------------------------------- small pieces */

export function Spinner({ label = 'Loading' }: { label?: string }) {
  return (
    <div role="status" className="flex items-center gap-2 p-6 text-muted">
      <span className="h-4 w-4 animate-spin rounded-full border-2 border-line border-t-marine" />
      {label}
    </div>
  )
}

export function Skeleton({ className = '' }: { className?: string }) {
  return <div className={`animate-pulse rounded-md bg-sunken ${className}`} />
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-line p-8 text-center">
      <p className="font-semibold">{title}</p>
      {children && <div className="mx-auto mt-1 max-w-md text-muted">{children}</div>}
    </div>
  )
}

export function ErrorNote({ error }: { error: unknown }) {
  const msg = error instanceof Error ? error.message : String(error)
  return (
    <div role="alert" className="rounded-md border border-brick/40 bg-brick/10 px-3 py-2 text-brick">
      {msg}
    </div>
  )
}

/* ---------------------------------------------------------------- toasts */

interface Toast { id: number; text: string; tone: 'ok' | 'warn' | 'error' }
const ToastCtx = createContext<(text: string, tone?: Toast['tone']) => void>(() => {})
export const useToast = () => useContext(ToastCtx)

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Toast[]>([])
  const push = useCallback((text: string, tone: Toast['tone'] = 'ok') => {
    const id = Date.now() + Math.random()
    setItems((t) => [...t, { id, text, tone }])
    setTimeout(() => setItems((t) => t.filter((x) => x.id !== id)), 5200)
  }, [])
  const value = useMemo(() => push, [push])
  const color = { ok: 'var(--pine)', warn: 'var(--signal)', error: 'var(--brick)' }
  return (
    <ToastCtx.Provider value={value}>
      {children}
      <div aria-live="polite" className="pointer-events-none fixed bottom-4 right-4 z-50 flex w-[min(380px,90vw)] flex-col gap-2">
        {items.map((t) => (
          <div key={t.id} className="rise pointer-events-auto rounded-md border bg-surface px-3 py-2 shadow-lift" style={{ borderColor: color[t.tone], borderLeftWidth: 4 }}>
            {t.text}
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  )
}
