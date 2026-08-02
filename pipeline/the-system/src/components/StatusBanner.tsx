import type { TeardownState } from '@/components/TeardownBanner'
import type { ConnectionStatus, TopologyGraph } from '@/types/topology'
import { cn } from '@/lib/cn'

type Props = {
  graph: TopologyGraph | null
  connectionStatus: ConnectionStatus
  teardown?: TeardownState
}

function connectionDotClass(status: ConnectionStatus): string {
  switch (status) {
    case 'connected':
      return 'bg-emerald-400 shadow-[0_0_8px_rgba(52,211,153,0.7)]'
    case 'reconnecting':
      return 'bg-amber-400 animate-pulse'
    default:
      return 'bg-slate-500'
  }
}

export function StatusBanner({ graph, connectionStatus, teardown }: Props) {
  const mode = (graph?.mode || '').toLowerCase()
  const modeLabel = mode === 'replay' ? 'REPLAY' : mode === 'record' ? 'RECORD' : mode ? mode.toUpperCase() : '—'
  const modeClass =
    mode === 'replay'
      ? 'bg-[#3b82f6]/15 text-[#3b82f6] border-[#3b82f6]/40'
      : mode === 'record'
        ? 'bg-violet-500/15 text-violet-300 border-violet-500/40'
        : 'bg-slate-500/15 text-slate-400 border-slate-600'

  const phase = teardown === 'deleted' ? 'Deleted' : teardown === 'deleting' ? 'Deleting' : graph?.phase
  const phaseHot = phase === 'Deleting' || phase === 'Deleted'

  return (
    <div className="flex flex-wrap items-center gap-3 border-b border-[#1f2937] bg-[#111827] px-4 py-2.5">
      <div className="flex items-center gap-2">
        <span
          className={cn('inline-block h-2.5 w-2.5 rounded-full', connectionDotClass(connectionStatus))}
          title={connectionStatus}
        />
        <span className="text-xs capitalize text-slate-400">{connectionStatus}</span>
      </div>

      <div className="h-4 w-px bg-[#1f2937]" />

      <div className="min-w-0">
        <span className="text-xs text-slate-500">Active test</span>
        <div className="truncate text-sm font-semibold text-white">
          {graph ? (
            <>
              <span className="text-slate-400">{graph.namespace}/</span>
              {graph.testName}
            </>
          ) : (
            <span className="font-normal text-slate-500">Waiting for stream…</span>
          )}
        </div>
      </div>

      <span className={cn('rounded border px-2 py-0.5 text-xs font-semibold tracking-wide', modeClass)}>
        {modeLabel}
      </span>

      <span className="rounded border border-[#1f2937] bg-[#090d16] px-2 py-0.5 text-xs text-slate-300">
        BootStep: {graph?.bootStep || '—'}
      </span>

      {phase && (
        <span
          className={cn(
            'rounded border px-2 py-0.5 text-xs',
            phaseHot
              ? 'border-red-500/50 bg-red-500/15 font-semibold text-red-300'
              : 'border-[#1f2937] bg-[#090d16] text-slate-300',
          )}
        >
          Phase: {phase}
        </span>
      )}
    </div>
  )
}
