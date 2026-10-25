import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, seedConflicts } from '../lib/api'
import type { Item } from '../lib/types'
import { useToast } from './ui'

interface Cmd { id: string; label: string; hint: string; run: () => void }

export function CommandPalette({ open, onClose }: { open: boolean; onClose: () => void }) {
  const nav = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()
  const [q, setQ] = useState('')
  const [sel, setSel] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const items = useQuery({ queryKey: ['items'], queryFn: () => api<Item[]>('/api/items'), enabled: open })

  useEffect(() => {
    if (open) {
      setQ('')
      setSel(0)
      setTimeout(() => inputRef.current?.focus(), 0)
    }
  }, [open])

  const cmds = useMemo<Cmd[]>(() => {
    const go = (to: string) => () => nav(to)
    const act = (fn: () => Promise<unknown>, ok: string) => () => {
      fn().then(() => { toast(ok); void qc.invalidateQueries() }).catch((e: Error) => toast(e.message, 'error'))
    }
    const base: Cmd[] = [
      { id: 'p', label: 'Go to Payouts', hint: 'page', run: go('/') },
      { id: 'a', label: 'Go to Approvals', hint: 'page', run: go('/approvals') },
      { id: 'c', label: 'Go to Conflicts', hint: 'page', run: go('/conflicts') },
      { id: 'b', label: 'Go to Batches', hint: 'page', run: go('/batches') },
      { id: 'bm', label: 'Go to Benchmark', hint: 'page', run: go('/benchmark') },
      { id: 'au', label: 'Go to Audit log', hint: 'page', run: go('/audit') },
      { id: 's', label: 'Go to Setup', hint: 'page', run: go('/setup') },
      { id: 'h', label: 'Go to Help and guide', hint: 'page', run: go('/help') },
      { id: 'rc', label: 'Reconcile now', hint: 'command', run: act(() => api('/api/admin/reconcile', { method: 'POST' }), 'Reconciled.') },
      { id: 'ds', label: 'Load demo data', hint: 'command', run: act(() => api('/api/demo/seed', { method: 'POST' }), 'Demo data loaded.') },
      { id: 'cp', label: 'Load conflict pack', hint: 'command', run: act(() => seedConflicts(), 'Conflict pack loaded.') },
    ]
    const people: Cmd[] = (items.data ?? []).filter((i) => i.attempt === 1).slice(0, 200).map((i) => ({ id: i.id, label: i.recipient_name, hint: `${i.status.toLowerCase()} · ${i.recipient_email}`, run: go(`/items/${i.id}`) }))
    return [...base, ...people]
  }, [nav, qc, toast, items.data])

  const shown = useMemo(() => {
    const t = q.trim().toLowerCase()
    return (t ? cmds.filter((c) => `${c.label} ${c.hint}`.toLowerCase().includes(t)) : cmds.slice(0, 11)).slice(0, 12)
  }, [cmds, q])

  if (!open) return null
  const run = (c?: Cmd) => { if (c) { onClose(); c.run() } }
  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-items-center bg-black/30 p-4 pt-[12vh] print:hidden" onClick={onClose}>
      <div role="dialog" aria-modal="true" aria-label="Command palette" className="panel rise w-[min(560px,100%)] overflow-hidden shadow-lift" onClick={(e) => e.stopPropagation()}>
        <input
          ref={inputRef} className="field !rounded-none !border-0 !border-b !py-3 !text-[15px]" placeholder="Type a page, a payout or a command" aria-label="Command"
          role="combobox" aria-expanded="true" aria-controls="palette-list" aria-activedescendant={shown[sel] ? `cmd-${shown[sel].id}` : undefined}
          value={q} onChange={(e) => { setQ(e.target.value); setSel(0) }}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown') { e.preventDefault(); setSel((s) => Math.min(s + 1, shown.length - 1)) }
            else if (e.key === 'ArrowUp') { e.preventDefault(); setSel((s) => Math.max(s - 1, 0)) }
            else if (e.key === 'Enter') { e.preventDefault(); run(shown[sel]) }
            else if (e.key === 'Escape') onClose()
          }}
        />
        <ul id="palette-list" role="listbox" className="max-h-[50vh] overflow-y-auto p-1">
          {shown.length === 0 && <li className="p-4 text-center text-muted">Nothing matches.</li>}
          {shown.map((c, i) => (
            <li key={c.id} id={`cmd-${c.id}`} role="option" aria-selected={i === sel} onMouseEnter={() => setSel(i)} onClick={() => run(c)}
              className={`flex cursor-pointer items-center justify-between gap-3 rounded-md px-3 py-2 ${i === sel ? 'bg-marine text-marine-ink' : ''}`}>
              <span className="font-semibold">{c.label}</span><span className={`truncate text-[12px] ${i === sel ? 'opacity-80' : 'text-muted'}`}>{c.hint}</span>
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}
