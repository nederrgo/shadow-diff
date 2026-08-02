import { useEffect, useRef, useState } from 'react'
import { tuskWsBase } from '@/lib/tuskBase'
import type { ConnectionStatus } from '@/types/topology'
import type { DiffSummary, DiffWsFrame } from '@/types/diffs'

const MIN_BACKOFF_MS = 1000
const MAX_BACKOFF_MS = 30_000

const emptySummary = (sessionId: string): DiffSummary => ({
  session_id: sessionId,
  total: 0,
  match: 0,
  mismatch: 0,
  voided: 0,
})

/**
 * Live session summary via Tusk `/ws/diffs`. On each verdict frame, callers
 * should refetch REST diffs for that trace (payloads stay off the wire).
 */
export function useDiffStream(sessionId: string) {
  const [summary, setSummary] = useState<DiffSummary | null>(null)
  const [lastVerdictTraceId, setLastVerdictTraceId] = useState<string | null>(null)
  const [connectionStatus, setConnectionStatus] = useState<ConnectionStatus>('disconnected')
  const backoffRef = useRef(MIN_BACKOFF_MS)

  useEffect(() => {
    if (!sessionId) {
      setSummary(null)
      setLastVerdictTraceId(null)
      setConnectionStatus('disconnected')
      return
    }

    let disposed = false
    let ws: WebSocket | null = null
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined

    const clearReconnect = () => {
      if (reconnectTimer !== undefined) {
        clearTimeout(reconnectTimer)
        reconnectTimer = undefined
      }
    }

    const scheduleReconnect = () => {
      if (disposed) return
      setConnectionStatus('reconnecting')
      const delay = backoffRef.current
      backoffRef.current = Math.min(backoffRef.current * 2, MAX_BACKOFF_MS)
      clearReconnect()
      reconnectTimer = setTimeout(connect, delay)
    }

    const connect = () => {
      if (disposed) return
      clearReconnect()
      try {
        ws = new WebSocket(`${tuskWsBase()}/ws/diffs?session_id=${encodeURIComponent(sessionId)}`)
      } catch {
        scheduleReconnect()
        return
      }

      ws.onopen = () => {
        if (disposed) return
        backoffRef.current = MIN_BACKOFF_MS
        setConnectionStatus('connected')
      }

      ws.onmessage = (ev) => {
        if (disposed) return
        try {
          const frame = JSON.parse(String(ev.data)) as DiffWsFrame
          if (frame.type === 'summary') {
            setSummary({
              session_id: frame.session_id,
              total: frame.total,
              match: frame.match,
              mismatch: frame.mismatch,
              voided: frame.voided,
            })
          } else if (frame.type === 'verdict') {
            setLastVerdictTraceId(frame.trace_id)
          }
        } catch {
          // ignore malformed frames
        }
      }

      ws.onerror = () => {
        // onclose handles reconnect
      }

      ws.onclose = () => {
        if (disposed) return
        scheduleReconnect()
      }
    }

    setSummary(emptySummary(sessionId))
    setLastVerdictTraceId(null)
    setConnectionStatus('reconnecting')
    connect()

    return () => {
      disposed = true
      clearReconnect()
      if (ws) {
        ws.onopen = null
        ws.onmessage = null
        ws.onerror = null
        ws.onclose = null
        ws.close()
      }
      setConnectionStatus('disconnected')
    }
  }, [sessionId])

  return { summary, lastVerdictTraceId, connectionStatus }
}
