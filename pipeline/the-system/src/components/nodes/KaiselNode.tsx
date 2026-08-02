import { Radar } from 'lucide-react'
import type { NodeProps } from '@xyflow/react'
import { BaseTopologyNode } from './BaseTopologyNode'
import type { TopologyNodeData } from '@/types/topology'

export function KaiselNode(props: NodeProps) {
  return <BaseTopologyNode {...props} data={props.data as TopologyNodeData} icon={Radar} accent="text-violet-400" />
}
