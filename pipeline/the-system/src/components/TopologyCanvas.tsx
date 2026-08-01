import { useEffect, useMemo } from 'react'
import {
  Background,
  Controls,
  MiniMap,
  ReactFlow,
  useEdgesState,
  useNodesState,
  type Edge,
  type Node,
  type NodeMouseHandler,
} from '@xyflow/react'
import { topologyNodeTypes } from '@/components/nodes'
import { positionFor } from '@/components/layoutPositions'
import type { TopologyGraph, TopologyNodeData } from '@/types/topology'

type Props = {
  graph: TopologyGraph | null
  onNodeClick: (nodeId: string) => void
}

function toFlowElements(graph: TopologyGraph | null): { nodes: Node[]; edges: Edge[] } {
  if (!graph) return { nodes: [], edges: [] }

  const nodes: Node[] = graph.nodes.map((n, i) => ({
    id: n.id,
    type: n.type in topologyNodeTypes ? n.type : 'role',
    position: positionFor(n.id, i),
    data: {
      label: n.label,
      status: n.status,
      nodeType: n.type,
      topologyId: n.id,
    } satisfies TopologyNodeData,
  }))

  const edges: Edge[] = graph.edges.map((e) => ({
    id: e.id,
    source: e.source,
    target: e.target,
    animated: e.animated,
    style: e.animated ? { stroke: '#3b82f6' } : undefined,
  }))

  return { nodes, edges }
}

export function TopologyCanvas({ graph, onNodeClick }: Props) {
  const mapped = useMemo(() => toFlowElements(graph), [graph])
  const [nodes, setNodes, onNodesChange] = useNodesState(mapped.nodes)
  const [edges, setEdges, onEdgesChange] = useEdgesState(mapped.edges)

  useEffect(() => {
    setNodes(mapped.nodes)
    setEdges(mapped.edges)
  }, [mapped, setNodes, setEdges])

  const handleNodeClick: NodeMouseHandler = (_ev, node) => {
    onNodeClick(node.id)
  }

  return (
    <div className="h-full w-full">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        nodeTypes={topologyNodeTypes}
        onNodeClick={handleNodeClick}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        proOptions={{ hideAttribution: true }}
        minZoom={0.35}
        maxZoom={1.75}
      >
        <Background gap={20} size={1} color="#1f2937" />
        <Controls showInteractive={false} />
        <MiniMap
          pannable
          zoomable
          nodeStrokeColor="#3b82f6"
          nodeColor="#111827"
          maskColor="rgba(9, 13, 22, 0.7)"
          className="!bg-[#111827] !border-[#1f2937]"
        />
      </ReactFlow>
    </div>
  )
}
