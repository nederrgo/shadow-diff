import { cn } from '@/lib/cn'
import type { NodeStatus } from '@/types/topology'

function statusBorderClass(status: string): string {
  switch (status as NodeStatus) {
    case 'Ready':
      return 'border-emerald-500 shadow-[0_0_0_1px_rgba(16,185,129,0.25)]'
    case 'Provisioning':
      return 'border-amber-400 animate-pulse'
    case 'Failed':
      return 'border-red-500 shadow-[0_0_0_1px_rgba(239,68,68,0.3)]'
    case 'Degraded':
      return 'border-amber-500'
    case 'Disabled':
      return 'border-slate-600 opacity-40'
    default:
      return 'border-slate-600'
  }
}

export function statusBadgeClass(status: string): string {
  switch (status as NodeStatus) {
    case 'Ready':
      return 'bg-emerald-500/15 text-emerald-400'
    case 'Provisioning':
      return 'bg-amber-500/15 text-amber-300'
    case 'Failed':
      return 'bg-red-500/15 text-red-400'
    case 'Degraded':
      return 'bg-amber-500/15 text-amber-300'
    case 'Disabled':
      return 'bg-slate-500/15 text-slate-400'
    default:
      return 'bg-slate-500/15 text-slate-400'
  }
}

export function nodeShellClass(status: string, extra?: string): string {
  return cn(
    'min-w-[160px] rounded-lg border-2 bg-[#111827] px-3 py-2 shadow-lg',
    statusBorderClass(status),
    extra,
  )
}
