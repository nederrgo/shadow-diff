import type { XYPosition } from '@xyflow/react'

/** Fixed OpenShift-style left-to-right layout keyed by Tusk node id. */
export const NODE_POSITIONS: Record<string, XYPosition> = {
  'target-app': { x: 0, y: 180 },
  kaisel: { x: 240, y: 180 },
  igris: { x: 480, y: 180 },
  'control-a': { x: 720, y: 40 },
  'control-b': { x: 720, y: 180 },
  candidate: { x: 720, y: 320 },
  shop: { x: 960, y: 80 },
  beru: { x: 960, y: 280 },
}

export function positionFor(id: string, index: number): XYPosition {
  return NODE_POSITIONS[id] ?? { x: 80 + (index % 4) * 220, y: 40 + Math.floor(index / 4) * 140 }
}
