import { Handle, Position, type NodeProps } from '@xyflow/react'
import { AlertTriangle, XCircle } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { nodeShellClass, statusBadgeClass } from './nodeStyles'
import type { TopologyNodeData } from '@/types/topology'

type Props = NodeProps & {
  data: TopologyNodeData
  icon: LucideIcon
  accent?: string
}

export function BaseTopologyNode({ data, icon: Icon, accent = 'text-[#3b82f6]', selected }: Props) {
  const status = String(data.status)
  const showError = status === 'Failed'
  const showWarn = status === 'Degraded'

  return (
    <div className={nodeShellClass(status, selected ? 'ring-2 ring-[#3b82f6]/50' : undefined)}>
      <Handle type="target" position={Position.Left} className="!h-2 !w-2 !border-0 !bg-slate-400" />
      <div className="flex items-start gap-2">
        <Icon className={`mt-0.5 h-4 w-4 shrink-0 ${accent}`} />
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium text-slate-100">{data.label}</div>
          <div className="mt-1 flex items-center gap-1.5">
            <span
              className={`inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${statusBadgeClass(status)}`}
            >
              {status}
            </span>
            {showError && <XCircle className="h-3.5 w-3.5 text-red-400" />}
            {showWarn && <AlertTriangle className="h-3.5 w-3.5 text-amber-400" />}
          </div>
        </div>
      </div>
      <Handle type="source" position={Position.Right} className="!h-2 !w-2 !border-0 !bg-slate-400" />
    </div>
  )
}
