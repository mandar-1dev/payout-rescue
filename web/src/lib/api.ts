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
    for (const n of ['item', 'action', 'reconciled', 'bench', 'conflict', 'note']) es.addEventListener(n, refresh(n))
    return () => es.close()
  }, [qc, onEvent])
}

export const listConflicts = (status?: string) =>
  api<import('./types').Conflict[]>(`/api/conflicts${status ? `?status=${status}` : ''}`)

export const getConflict = (id: string) =>
  api<import('./types').Conflict>(`/api/conflicts/${id}`)

export const resolveConflict = (id: string, body: { resolution: string; approver: string; value?: string; note?: string }) =>
  api<{ status: string }>(`/api/conflicts/${id}/resolve`, { method: 'POST', json: body })

export const seedConflicts = () =>
  api<{ status: string }>('/api/demo/seed-conflicts', { method: 'POST' })

export const addNote = (itemId: string, author: string, body: string) =>
  api<import('./types').Note>(`/api/items/${itemId}/notes`, { method: 'POST', json: { author, body } })

export const snoozeAction = (actionId: string, days: number) =>
  api<{ status: string }>(`/api/actions/${actionId}/snooze`, { method: 'POST', json: { days } })

export const bulkAcknowledge = (actionIds: string[], approver: string) =>
  api<{ acknowledged: number }>('/api/actions/bulk-acknowledge', { method: 'POST', json: { action_ids: actionIds, approver } })

export const getReference = () =>
  api<import('./types').Reference>('/api/reference')

export const getDemoPayouts = () => api<import('./types').DemoPayouts>('/api/demo/payouts')

export const checkDuplicates = (recipients: { email: string; amount: string; currency: string }[]) =>
  api<{ duplicates: import('./types').DuplicateWarning[] }>('/api/batches/check', { method: 'POST', json: { recipients } })

/** Downloads a file from an authenticated endpoint (a plain link cannot send the token). */
export async function download(path: string, filename: string): Promise<void> {
  const res = await fetch(path, { headers: { Authorization: `Bearer ${getToken()}` } })
  if (!res.ok) throw new ApiError(`Download failed (${res.status})`, res.status)
  const url = URL.createObjectURL(await res.blob())
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}
