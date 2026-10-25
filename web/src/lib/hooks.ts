import { useCallback, useEffect, useState } from 'react'

const KEY = 'payoutrescue.approver'
/** The name recorded in the audit log next to every approval. */
export function useApprover(): [string, (v: string) => void] {
  const [name, setName] = useState(() => localStorage.getItem(KEY) ?? '')
  const set = useCallback((v: string) => {
    localStorage.setItem(KEY, v)
    setName(v)
  }, [])
  return [name, set]
}

/** Global keyboard shortcuts. Ignored while typing in a field. */
export function useShortcuts(handlers: { palette: () => void; help: () => void; go: (to: string) => void }) {
  const { palette, help, go } = handlers
  useEffect(() => {
    let pending = false
    let timer = 0
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        palette()
        return
      }
      const t = e.target as HTMLElement | null
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return
      if (e.ctrlKey || e.metaKey || e.altKey) return
      if (e.key === '?') {
        e.preventDefault()
        help()
        return
      }
      if (pending) {
        pending = false
        window.clearTimeout(timer)
        const to = ({ p: '/', a: '/approvals', c: '/conflicts', b: '/batches' } as Record<string, string>)[e.key.toLowerCase()]
        if (to) go(to)
        return
      }
      if (e.key.toLowerCase() === 'g') {
        pending = true
        timer = window.setTimeout(() => { pending = false }, 1200)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => { window.removeEventListener('keydown', onKey); window.clearTimeout(timer) }
  }, [palette, help, go])
}
