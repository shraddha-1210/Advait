import { useCallback, useEffect, useRef, useState } from 'react'
import type { ApiResult } from '../api/client'

export interface ApiState<T> {
  data: T | null
  error: string | null
  /** true until the first response (success or failure) arrives */
  loading: boolean
  /** true while any request is in flight, including background refreshes */
  refreshing: boolean
  reload: () => Promise<ApiResult<T>>
}

/**
 * Fetches on mount and on reload(). Optional polling. `fetcher` must be a
 * stable function (the module-level functions in api/client.ts are). On an error the last
 * good data is dropped, so the UI never shows stale numbers as if current.
 */
export function useApi<T>(fetcher: () => Promise<ApiResult<T>>, pollMs?: number): ApiState<T> {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const seq = useRef(0)

  const reload = useCallback(async () => {
    const my = ++seq.current
    setRefreshing(true)
    const r = await fetcher()
    if (my === seq.current) {
      if (r.ok) {
        setData(r.data)
        setError(null)
      } else {
        setData(null)
        setError(r.error)
      }
      setLoading(false)
      setRefreshing(false)
    }
    return r
  }, [fetcher])

  useEffect(() => {
    void reload()
    if (!pollMs) return
    const id = window.setInterval(() => void reload(), pollMs)
    return () => window.clearInterval(id)
  }, [reload, pollMs])

  return { data, error, loading, refreshing, reload }
}
