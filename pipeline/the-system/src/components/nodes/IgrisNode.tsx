import { GitBranch } from 'lucide-react'
import type { NodeProps } from '@xyflow/react'
import { BaseTopologyNode } from './BaseTopologyNode'
import type { TopologyNodeData } from '@/types/topology'

export function IgrisNode(props: NodeProps) {
  return (
    <BaseTopologyNode {...props} data={props.data as TopologyNodeData} icon={GitBranch} accent="text-cyan-400" />
  )
}
