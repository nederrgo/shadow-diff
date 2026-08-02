import { Layers } from 'lucide-react'
import type { NodeProps } from '@xyflow/react'
import { BaseTopologyNode } from './BaseTopologyNode'
import type { TopologyNodeData } from '@/types/topology'

export function ShadowRoleNode(props: NodeProps) {
  return (
    <BaseTopologyNode {...props} data={props.data as TopologyNodeData} icon={Layers} accent="text-[#3b82f6]" />
  )
}
