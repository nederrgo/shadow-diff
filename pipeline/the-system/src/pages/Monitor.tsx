import { useCallback, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useTopologyStream } from '@/hooks/useTopologyStream'
import { StatusBanner } from '@/components/StatusBanner'
import { TeardownBanner } from '@/components/TeardownBanner'
import { TestPicker } from '@/components/TestPicker'
import { TopologyCanvas } from '@/components/TopologyCanvas'
import { NodeDrawer } from '@/components/NodeDrawer'
import type { TopologyNode } from '@/types/topology'

export default function Monitor() {
  const [params, setParams] = useSearchParams()
  const testName = params.get('test') ?? ''
  const namespace = params.get('namespace') ?? ''

  const { graph, tests, teardown, connectionStatus } = useTopologyStream(namespace, testName)
  const [selectedId, setSelectedId] = useState<string | null>(null)

  const selectedNode: TopologyNode | null = useMemo(() => {
    if (!selectedId || !graph) return null
    return graph.nodes.find((n) => n.id === selectedId) ?? null
  }, [graph, selectedId])

  const selectTest = useCallback(
    (ns: string, name: string) => {
      const next = new URLSearchParams(params)
      next.set('namespace', ns)
      next.set('test', name)
      setParams(next, { replace: true })
      setSelectedId(null)
    },
    [params, setParams],
  )

  const bannerState = teardown ?? (graph?.phase === 'Deleting' ? 'deleting' : null)

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex flex-wrap items-end gap-3 border-b border-[#1f2937] bg-[#090d16] px-4 py-3">
        <TestPicker
          tests={tests}
          namespace={namespace}
          testName={testName}
          onSelect={selectTest}
        />
        <p className="pb-1.5 text-xs text-slate-500">
          Stream: <code className="text-slate-400">ws://…:8082/ws/monitor</code> (all tests)
          {tests.length > 0 ? ` · ${tests.length} live` : null}
        </p>
      </div>

      <StatusBanner graph={graph} connectionStatus={connectionStatus} teardown={bannerState} />

      <div className="flex min-h-0 flex-1 flex-col">
        <TeardownBanner
          state={bannerState}
          namespace={graph?.namespace || namespace}
          testName={graph?.testName || testName}
        />
        <div className="relative min-h-0 flex-1">
          {!namespace || !testName ? (
            <div className="flex h-full items-center justify-center text-sm text-slate-500">
              Open the menu and pick a running ShadowTest.
            </div>
          ) : !graph ? (
            <div className="flex h-full items-center justify-center text-sm text-slate-500">
              Waiting for <span className="mx-1 text-slate-300">{namespace}/{testName}</span>…
            </div>
          ) : (
            <>
              <TopologyCanvas graph={graph} onNodeClick={setSelectedId} />
              <NodeDrawer node={selectedNode} graph={graph} onClose={() => setSelectedId(null)} />
            </>
          )}
        </div>
      </div>
    </div>
  )
}
