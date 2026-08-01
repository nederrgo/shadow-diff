import { useCallback, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useTopologyStream } from '@/hooks/useTopologyStream'
import { StatusBanner } from '@/components/StatusBanner'
import { TeardownBanner } from '@/components/TeardownBanner'
import { TopologyCanvas } from '@/components/TopologyCanvas'
import { NodeDrawer } from '@/components/NodeDrawer'
import type { TopologyNode } from '@/types/topology'

export default function Monitor() {
  const [params, setParams] = useSearchParams()
  const testName = params.get('test') ?? ''
  const namespace = params.get('namespace') ?? ''

  const { graph, teardown, connectionStatus } = useTopologyStream(testName, namespace)
  const [selectedId, setSelectedId] = useState<string | null>(null)

  const selectedNode: TopologyNode | null = useMemo(() => {
    if (!selectedId || !graph) return null
    return graph.nodes.find((n) => n.id === selectedId) ?? null
  }, [graph, selectedId])

  const updateFilter = useCallback(
    (key: 'test' | 'namespace', value: string) => {
      const next = new URLSearchParams(params)
      if (value) next.set(key, value)
      else next.delete(key)
      setParams(next, { replace: true })
    },
    [params, setParams],
  )

  const bannerState = teardown ?? (graph?.phase === 'Deleting' ? 'deleting' : null)

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex flex-wrap items-end gap-3 border-b border-[#1f2937] bg-[#090d16] px-4 py-3">
        <label className="flex flex-col gap-1 text-xs text-slate-500">
          Namespace
          <input
            className="w-44 rounded border border-[#1f2937] bg-[#111827] px-2 py-1.5 text-sm text-slate-100 outline-none focus:border-[#3b82f6]"
            value={namespace}
            placeholder="default"
            onChange={(e) => updateFilter('namespace', e.target.value.trim())}
          />
        </label>
        <label className="flex flex-col gap-1 text-xs text-slate-500">
          ShadowTest
          <input
            className="w-56 rounded border border-[#1f2937] bg-[#111827] px-2 py-1.5 text-sm text-slate-100 outline-none focus:border-[#3b82f6]"
            value={testName}
            placeholder="my-app-shadow"
            onChange={(e) => updateFilter('test', e.target.value.trim())}
          />
        </label>
        <p className="pb-1.5 text-xs text-slate-500">
          Stream: <code className="text-slate-400">ws://…:8082/ws/monitor</code>
          {!testName && !namespace ? ' (all tests)' : null}
        </p>
      </div>

      <StatusBanner graph={graph} connectionStatus={connectionStatus} teardown={bannerState} />

      {/* Banner is a flex sibling above the canvas — React Flow covers absolute overlays. */}
      <div className="flex min-h-0 flex-1 flex-col">
        <TeardownBanner
          state={bannerState}
          namespace={graph?.namespace || namespace}
          testName={graph?.testName || testName}
        />
        <div className="relative min-h-0 flex-1">
          <TopologyCanvas graph={graph} onNodeClick={setSelectedId} />
          <NodeDrawer node={selectedNode} graph={graph} onClose={() => setSelectedId(null)} />
        </div>
      </div>
    </div>
  )
}
