import { FlaskConical } from 'lucide-react'
import type { NodeProps } from '@xyflow/react'
import { BaseTopologyNode } from './BaseTopologyNode'
import type { TopologyNodeData } from '@/types/topology'

export function BeruNode(props: NodeProps) {
  return (
    <BaseTopologyNode
      {...props}
      data={props.data as TopologyNodeData}
      icon={FlaskConical}
      accent="text-emerald-400"
    />
  )
}
