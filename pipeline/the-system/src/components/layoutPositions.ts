import type { XYPosition } from '@xyflow/react'

/** Fixed OpenShift-style left-to-right layout keyed by Tusk node id. */
const NODE_POSITIONS: Record<string, XYPosition> = {
  'target-app': { x: 0, y: 180 },
  // AMQP path sits on the midline (target → queue → igris); Kaisel sits above
  // and only feeds Shop for egress seed.
  'prod-amqp': { x: 220, y: 180 },
  kaisel: { x: 220, y: 40 },
  igris: { x: 520, y: 180 },
  'control-a': { x: 900, y: 40 },
  'control-b': { x: 900, y: 180 },
  candidate: { x: 900, y: 320 },
  shop: { x: 1140, y: 40 },
  beru: { x: 1140, y: 280 },
}

export function positionFor(id: string, index: number): XYPosition {
  return NODE_POSITIONS[id] ?? { x: 80 + (index % 4) * 220, y: 40 + Math.floor(index / 4) * 140 }
}
