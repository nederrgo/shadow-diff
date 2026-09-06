export type NodeStatus = 'Ready' | 'Provisioning' | 'Failed' | 'Disabled' | 'Degraded'

type NodeType = 'target' | 'capture' | 'amqp' | 'ingress' | 'egress' | 'sink' | 'role'

export type ConnectionStatus = 'connected' | 'reconnecting' | 'disconnected'

export interface TopologyNode {
  id: string
  type: NodeType | string
  label: string
  status: NodeStatus | string
}

interface TopologyEdge {
  id: string
  source: string
  target: string
  animated: boolean
}

export interface TopologyGraph {
  testName: string
  namespace: string
  phase: string
  bootStep: string
  mode: string
  message?: string
  nodes: TopologyNode[]
  edges: TopologyEdge[]
}

/** Data payload attached to each React Flow node. */
export interface TopologyNodeData extends Record<string, unknown> {
  label: string
  status: NodeStatus | string
  nodeType: NodeType | string
  topologyId: string
}
