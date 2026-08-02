import { Box } from 'lucide-react'
import type { NodeProps } from '@xyflow/react'
import { BaseTopologyNode } from './BaseTopologyNode'
import type { TopologyNodeData } from '@/types/topology'

export function TargetAppNode(props: NodeProps) {
  return <BaseTopologyNode {...props} data={props.data as TopologyNodeData} icon={Box} accent="text-sky-400" />
}
