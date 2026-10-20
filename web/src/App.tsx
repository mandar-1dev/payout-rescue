import { useEffect, useState, type ReactNode } from 'react'
import { NavLink, Route, Routes } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, clearToken, useLiveUpdates, getToken } from './lib/api'
import { useApprover } from './lib/hooks'
import type { Session, Summary } from './lib/types'
import { Mark } from './components/Brand'
import { Spinner } from './components/ui'
import { Login } from './pages/Login'
import { Dashboard } from './pages/Dashboard'
import { ItemPage } from './pages/ItemPage'
import { Approvals } from './pages/Approvals'
import { Batches } from './pages/Batches'
import { Benchmark } from './pages/Benchmark'
import { Audit } from './pages/Audit'
import { Setup } from './pages/Setup'

type PublicInfo = { auth_required: boolean; platform: string; env: string }

function Icon({ d }: { d: ReactNode }) {
  return <svg width="18" height="18" viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{d}</svg>
}

const nav = [
  { to: '/', label: 'Payouts', end: true, icon: <path d="M3 5h14M3 10h14M3 15h9" /> },
  { to: '/approvals', label: 'Approvals', icon: <path d="M4 10.5l3.5 3.5L16 6" /> },
  { to: '/batches', label: 'Batches', icon: <><rect x="3" y="4" width="14" height="12" rx="2" /><path d="M10 7.5v5M7.5 10h5" /></> },
  { to: '/benchmark', label: 'Benchmark', icon: <path d="M4 16V9M10 16V4M16 16v-5" /> },
  { to: '/audit', label: 'Audit log', icon: <><path d="M5 3h8l3 3v11H5z" /><path d="M8 10h5M8 13h5" /></> },
  { to: '/setup', label: 'Setup', icon: <><circle cx="10" cy="10" r="2.6" /><path d="M10 3v2.2M10 14.8V17M3 10h2.2M14.8 10H17M5.2 5.2l1.6 1.6M13.2 13.2l1.6 1.6M14.8 5.2l-1.6 1.6M6.8 13.2l-1.6 1.6" /></> },
]

function Shell({ session, onSignOut }: { session: Session; onSignOut: () => void }) {
  const [approver, setApprover] = useApprover()
  const [theme, setTheme] = useState<'light' | 'dark'>(() => (document.documentElement.dataset.theme as 'light' | 'dark') || 'light')
  const summary = useQuery({ queryKey: ['limbo'], queryFn: () => api<Summary>('/api/limbo') })
  useLiveUpdates()
  const qc = useQueryClient()

  useEffect(() => {
    document.documentElement.dataset.theme = theme
    localStorage.setItem('payoutrescue.theme', theme)
  }, [theme])

  const pending = summary.data?.needing_action ?? 0
  return (
    <div className="grid min-h-full grid-cols-1 md:grid-cols-[236px_1fr]">
      <aside className="flex flex-col border-b border-line bg-surface md:sticky md:top-0 md:h-screen md:border-b-0 md:border-r">
        <div className="flex items-center gap-2.5 px-4 py-4">
          <Mark />
          <div className="leading-tight">
            <p className="font-bold">PayoutRescue</p>
            <p className="text-[12px] text-muted">{session.platform}</p>
          </div>
        </div>
        <nav aria-label="Main" className="flex gap-1 overflow-x-auto px-2 pb-2 md:flex-1 md:flex-col md:overflow-visible">
          {nav.map((n) => (
            <NavLink key={n.to} to={n.to} end={n.end} className={({ isActive }) => `flex items-center gap-2.5 whitespace-nowrap rounded-md px-3 py-2 font-semibold ${isActive ? 'bg-marine text-marine-ink' : 'text-ink hover:bg-sunken'}`}>
              <Icon d={n.icon} />
              {n.label}
              {n.to === '/approvals' && pending > 0 && <span className="ml-auto rounded-full bg-signal px-1.5 text-[11px] font-bold text-white num">{pending}</span>}
            </NavLink>
          ))}
        </nav>
        <div className="hidden space-y-2 border-t border-line p-3 md:block">
          <label className="block">
            <span className="mb-1 block text-[12px] font-semibold text-muted">Acting as</span>
            <input className="field" placeholder="Your name" value={approver} onChange={(e) => setApprover(e.target.value)} aria-label="Your name, recorded with every approval" />
          </label>
          <div className="flex gap-2">
            <button className="btn btn-quiet flex-1" onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')} aria-label="Toggle dark mode">{theme === 'dark' ? 'Light' : 'Dark'}</button>
            {session.env !== 'development' || getToken() ? <button className="btn btn-quiet flex-1" onClick={() => { clearToken(); qc.clear(); onSignOut() }}>Sign out</button> : null}
          </div>
        </div>
      </aside>

      <div className="min-w-0">
        <header className="sticky top-0 z-30 flex flex-wrap items-center gap-3 border-b border-line bg-bg/90 px-5 py-3 backdrop-blur">
          <span className="rounded-md bg-signal/15 px-2 py-0.5 text-[12px] font-bold tracking-wide text-signal" title="No real money moves in this app">SANDBOX</span>
          <span className="text-muted">Nothing here moves real money.</span>
          <div className="ml-auto md:hidden">
            <input className="field !w-40" placeholder="Your name" value={approver} onChange={(e) => setApprover(e.target.value)} aria-label="Your name, recorded with every approval" />
          </div>
        </header>
        {session.webhook_bypass && (
          <div role="alert" className="border-b border-signal/40 bg-signal/10 px-5 py-2 text-signal">Webhook signature checks are OFF. Use this only with sandbox simulator events.</div>
        )}
        <main className="mx-auto max-w-[1280px] p-5">
          <Routes>
            <Route path="/" element={<Dashboard />} />
            <Route path="/items/:id" element={<ItemPage approver={approver} />} />
            <Route path="/approvals" element={<Approvals approver={approver} />} />
            <Route path="/batches" element={<Batches />} />
            <Route path="/benchmark" element={<Benchmark />} />
            <Route path="/audit" element={<Audit />} />
            <Route path="/setup" element={<Setup />} />
            <Route path="*" element={<p className="p-6 text-muted">That page does not exist.</p>} />
          </Routes>
        </main>
      </div>
    </div>
  )
}

export default function App() {
  const [authed, setAuthed] = useState<boolean | null>(null)
  const pub = useQuery({ queryKey: ['public'], queryFn: () => api<PublicInfo>('/api/public') })
  const session = useQuery({ queryKey: ['session'], queryFn: () => api<Session>('/api/session'), enabled: pub.isSuccess && authed !== false, retry: false })

  useEffect(() => {
    const on = () => setAuthed(false)
    window.addEventListener('payoutrescue:unauthorized', on)
    return () => window.removeEventListener('payoutrescue:unauthorized', on)
  }, [])
  useEffect(() => { if (session.isSuccess) setAuthed(true) }, [session.isSuccess])

  if (pub.isLoading) return <Spinner label="Starting" />
  if (pub.error || !pub.data) return <p role="alert" className="p-6 text-brick">Could not reach the server.</p>
  if (authed === false || (session.isError && pub.data.auth_required)) {
    return <Login platform={pub.data.platform} onDone={() => { setAuthed(true); void session.refetch() }} />
  }
  if (!session.data) return <Spinner label="Opening the desk" />
  return <Shell session={session.data} onSignOut={() => setAuthed(false)} />
}
