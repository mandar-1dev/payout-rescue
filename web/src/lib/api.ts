import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'

const KEY = 'payoutrescue.token'
export const getToken = () => localStorage.getItem(KEY) ?? ''
export const setToken = (t: string) => localStorage.setItem(KEY, t)
export const clearToken = () => localStorage.removeItem(KEY)

export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

export async function api<T>(path: string, init: RequestInit & { json?: unknown } = {}): Promise<T> {
  const headers = new Headers(init.headers)
  const token = getToken()
  if (token) headers.set('Authorization', `Bearer ${token}`)
  let body = init.body
  if (init.json !== undefined) {
    headers.set('Content-Type', 'application/json')
    body = JSON.stringify(init.json)
  }
  const res = await fetch(path, { ...init, headers, body })
  const text = await res.text()
  const data = text ? JSON.parse(text) : null
  if (res.status === 401) window.dispatchEvent(new Event('payoutrescue:unauthorized'))
  if (!res.ok) throw new ApiError((data && data.error) || `Request failed (${res.status})`, res.status)
  return data as T
}

/** Refreshes data when the server reports a change (server-sent events). */
export function useLiveUpdates(onEvent?: (name: string) => void) {
  const qc = useQueryClient()
  useEffect(() => {
    const token = getToken()
    const es = new EventSource(`/api/events${token ? `?token=${encodeURIComponent(token)}` : ''}`)
    const refresh = (name: string) => () => {
      void qc.invalidateQueries()
      onEvent?.(name)
    }
    for (const n of ['item', 'action', 'reconciled', 'bench']) es.addEventListener(n, refresh(n))
    return () => es.close()
  }, [qc, onEvent])
}
