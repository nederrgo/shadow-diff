import { useEffect, useRef, useState } from 'react'
import type { TeardownState } from '@/components/TeardownBanner'
import type { ConnectionStatus, TopologyGraph } from '@/types/topology'

const MIN_BACKOFF_MS = 1000
const MAX_BACKOFF_MS = 30_000

function buildWsUrl(testName: string, namespace: string): string {
  const params = new URLSearchParams()
  if (testName) params.set('test', testName)
  if (namespace) params.set('namespace', namespace)
  const qs = params.toString()
  const host = window.location.hostname
  return `ws://${host}:8082/ws/monitor${qs ? `?${qs}` : ''}`
}

/**
 * Subscribes to Tusk's topology WebSocket and reconnects with exponential backoff.
 * Keeps the last live graph through Deleting/Deleted so the canvas does not blank.
 */
export function useTopologyStream(testName: string, namespace: string) {
  const [graph, setGraph] = useState<TopologyGraph | null>(null)
  const [teardown, setTeardown] = useState<TeardownState>(null)
  const [connectionStatus, setConnectionStatus] = useState<ConnectionStatus>('disconnected')
  const backoffRef = useRef(MIN_BACKOFF_MS)

  useEffect(() => {
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
      const url = buildWsUrl(testName, namespace)
      try {
        ws = new WebSocket(url)
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
          const next = JSON.parse(String(ev.data)) as TopologyGraph
          // Tombstone: keep the last topology under the banner; do not wipe the canvas.
          if (next.phase === 'Deleted') {
            setTeardown('deleted')
            return
          }
          if (next.phase === 'Deleting') {
            setTeardown('deleting')
            setGraph(next)
            return
          }
          setTeardown(null)
          setGraph(next)
        } catch {
          // ponytail: ignore malformed frames; next whole-state frame supersedes
        }
      }

      ws.onerror = () => {
        // onclose handles reconnect; browsers fire both
      }

      ws.onclose = () => {
        if (disposed) return
        scheduleReconnect()
      }
    }

    setConnectionStatus('reconnecting')
    setTeardown(null)
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
  }, [testName, namespace])

  return { graph, teardown, connectionStatus }
}
