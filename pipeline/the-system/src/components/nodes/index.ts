import type { NodeTypes } from '@xyflow/react'
import { TargetAppNode } from './TargetAppNode'
import { KaiselNode } from './KaiselNode'
import { IgrisNode } from './IgrisNode'
import { ShopNode } from './ShopNode'
import { BeruNode } from './BeruNode'
import { ShadowRoleNode } from './ShadowRoleNode'

/** React Flow nodeTypes keyed by Tusk semantic `type` strings. */
export const topologyNodeTypes: NodeTypes = {
  target: TargetAppNode,
  capture: KaiselNode,
  ingress: IgrisNode,
  egress: ShopNode,
  sink: BeruNode,
  role: ShadowRoleNode,
}
