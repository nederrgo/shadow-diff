import { Store } from 'lucide-react'
import type { NodeProps } from '@xyflow/react'
import { BaseTopologyNode } from './BaseTopologyNode'
import type { TopologyNodeData } from '@/types/topology'

export function ShopNode(props: NodeProps) {
  return <BaseTopologyNode {...props} data={props.data as TopologyNodeData} icon={Store} accent="text-orange-400" />
}
