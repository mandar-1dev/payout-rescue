import { useCallback, useState } from 'react'

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
