import { X } from 'lucide-react'
import type { TopologyGraph, TopologyNode } from '@/types/topology'
import { statusBadgeClass } from '@/components/nodes/nodeStyles'

type Props = {
  node: TopologyNode | null
  graph: TopologyGraph | null
  onClose: () => void
}

function statusHint(status: string): string {
  switch (status) {
    case 'Ready':
      return 'Component is ready; traffic can flow on connected Ready edges.'
    case 'Provisioning':
      return 'Still provisioning. Wait for Monarch boot steps to complete.'
    case 'Failed':
      return 'Component failed during this ShadowTest run.'
    case 'Disabled':
      return 'Not participating in the current mode (record vs replay).'
    case 'Degraded':
      return 'Capture rule is active but reporting a degraded state.'
    default:
      return 'No additional detail from Tusk for this status.'
  }
}

export function NodeDrawer({ node, graph, onClose }: Props) {
  if (!node) return null

  const message = graph?.message?.trim()

  return (
    <aside className="absolute inset-y-0 right-0 z-20 flex w-80 flex-col border-l border-[#1f2937] bg-[#111827] shadow-2xl">
      <div className="flex items-center justify-between border-b border-[#1f2937] px-4 py-3">
        <h2 className="text-sm font-semibold text-white">Node details</h2>
        <button
          type="button"
          onClick={onClose}
          className="rounded p-1 text-slate-400 hover:bg-white/5 hover:text-white"
          aria-label="Close drawer"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      <div className="flex-1 space-y-4 overflow-y-auto px-4 py-4 text-sm">
        <Field label="ID" value={node.id} mono />
        <Field label="Label" value={node.label} />
        <Field label="Type" value={node.type} mono />
        <div>
          <div className="mb-1 text-xs uppercase tracking-wide text-slate-500">Status</div>
          <span
            className={`inline-flex rounded px-2 py-0.5 text-xs font-semibold uppercase ${statusBadgeClass(node.status)}`}
          >
            {node.status}
          </span>
        </div>
        <div>
          <div className="mb-1 text-xs uppercase tracking-wide text-slate-500">Message</div>
          {message ? (
            <p className="rounded border border-[#1f2937] bg-[#090d16] px-3 py-2 text-slate-200">{message}</p>
          ) : (
            <p className="text-slate-400">{statusHint(node.status)}</p>
          )}
          <p className="mt-2 text-xs text-slate-500">
            Tusk attaches progress/error detail at the graph level (`TopologyGraph.message`), not per node.
          </p>
        </div>
      </div>
    </aside>
  )
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <div className="mb-1 text-xs uppercase tracking-wide text-slate-500">{label}</div>
      <div className={mono ? 'font-mono text-slate-200' : 'text-slate-200'}>{value}</div>
    </div>
  )
}
