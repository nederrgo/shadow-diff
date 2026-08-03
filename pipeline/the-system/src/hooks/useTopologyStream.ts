import { useEffect, useMemo, useRef, useState } from 'react'
import type { TeardownState } from '@/components/TeardownBanner'
import { tuskWsBase } from '@/lib/tuskBase'
import type { ConnectionStatus, TopologyGraph } from '@/types/topology'

const MIN_BACKOFF_MS = 1000
const MAX_BACKOFF_MS = 30_000

export type CatalogEntry = {
  namespace: string
  testName: string
  phase: string
  mode: string
}

function catalogKey(namespace: string, testName: string): string {
  return `${namespace}/${testName}`
}

function buildWsUrl(): string {
  // Unfiltered stream: Tusk sends every ShadowTest; the UI picks locally.
  return `${tuskWsBase()}/ws/monitor`
}

/**
 * Watches every ShadowTest on one WebSocket, keeps a local catalog, and exposes
 * the graph for the URL-selected test. Selection changes do not reconnect.
 */
export function useTopologyStream(selectedNamespace: string, selectedTest: string) {
  const [catalog, setCatalog] = useState<Record<string, TopologyGraph>>({})
  const [teardown, setTeardown] = useState<TeardownState>(null)
  const [connectionStatus, setConnectionStatus] = useState<ConnectionStatus>('disconnected')
  const backoffRef = useRef(MIN_BACKOFF_MS)
  const selectionRef = useRef({ namespace: selectedNamespace, testName: selectedTest })
  selectionRef.current = { namespace: selectedNamespace, testName: selectedTest }

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

    const isSelected = (namespace: string, testName: string) => {
      const sel = selectionRef.current
      return sel.namespace === namespace && sel.testName === testName
    }

    const connect = () => {
      if (disposed) return
      clearReconnect()
      try {
        ws = new WebSocket(buildWsUrl())
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
          const key = catalogKey(next.namespace, next.testName)
          const selected = isSelected(next.namespace, next.testName)

          if (next.phase === 'Deleted') {
            setCatalog((prev) => {
              if (!(key in prev)) return prev
              const { [key]: _, ...rest } = prev
              return rest
            })
            if (selected) setTeardown('deleted')
            return
          }

          setCatalog((prev) => ({ ...prev, [key]: next }))
          if (selected) {
            setTeardown(next.phase === 'Deleting' ? 'deleting' : null)
          }
        } catch {
          // ponytail: ignore malformed frames; next whole-state frame supersedes
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
  }, [])

  // Clear teardown when the user picks a different test.
  useEffect(() => {
    setTeardown(null)
  }, [selectedNamespace, selectedTest])

  // Mirror Deleting from the cached graph for the current selection.
  // Missing entry is left alone so a Deleted tombstone from the WS handler sticks.
  useEffect(() => {
    if (!selectedNamespace || !selectedTest) return
    const g = catalog[catalogKey(selectedNamespace, selectedTest)]
    if (!g) return
    setTeardown(g.phase === 'Deleting' ? 'deleting' : null)
  }, [catalog, selectedNamespace, selectedTest])

  const graph = useMemo(() => {
    if (!selectedNamespace || !selectedTest) return null
    return catalog[catalogKey(selectedNamespace, selectedTest)] ?? null
  }, [catalog, selectedNamespace, selectedTest])

  const tests = useMemo((): CatalogEntry[] => {
    return Object.values(catalog)
      .map((g) => ({
        namespace: g.namespace,
        testName: g.testName,
        phase: g.phase,
        mode: g.mode,
      }))
      .sort((a, b) => {
        const ns = a.namespace.localeCompare(b.namespace)
        return ns !== 0 ? ns : a.testName.localeCompare(b.testName)
      })
  }, [catalog])

  return { graph, tests, teardown, connectionStatus }
}
