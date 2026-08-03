import { useEffect, useMemo, useRef, useState } from 'react'
import { cn } from '@/lib/cn'
import type { ReplayExecution } from '@/types/diffs'

type Props = {
  executions: ReplayExecution[]
  /** Empty string means "latest" (resolved by Tusk). */
  executionId: string
  onSelect: (executionId: string) => void
  disabled?: boolean
}

function shortExec(id: string): string {
  if (id.length <= 18) return id
  return id.slice(0, 18) + '…'
}

function execSub(e: ReplayExecution): string {
  if (!e.created_at) return e.replay_execution_id
  try {
    return `${e.replay_execution_id} · ${new Date(e.created_at).toLocaleString()}`
  } catch {
    return e.replay_execution_id
  }
}

export function ExecutionPicker({ executions, executionId, onSelect, disabled }: Props) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)

  const latest = executions[0]
  const selected = executionId
    ? executions.find((e) => e.replay_execution_id === executionId)
    : latest

  const selectedLabel = useMemo(() => {
    if (!latest) return 'No replay runs'
    if (!executionId || (latest && executionId === latest.replay_execution_id)) {
      return `Latest (${shortExec(latest.replay_execution_id)})`
    }
    return selected ? shortExec(selected.replay_execution_id) : shortExec(executionId)
  }, [executionId, latest, selected])

  useEffect(() => {
    if (!open) return
    const onDoc = (ev: MouseEvent) => {
      if (!rootRef.current?.contains(ev.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [open])

  return (
    <div ref={rootRef} className="relative min-w-[12rem] max-w-xs">
      <span className="mb-1 block text-xs text-slate-500">Replay run</span>
      <button
        type="button"
        disabled={disabled || executions.length === 0}
        className={cn(
          'flex w-full items-center justify-between gap-2 rounded border border-[#1f2937] bg-[#111827] px-3 py-1.5 text-left text-sm',
          'text-slate-100 outline-none hover:border-[#3b82f6]/50 focus:border-[#3b82f6]',
          (disabled || executions.length === 0) && 'cursor-not-allowed opacity-50',
        )}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <span className="truncate font-mono text-xs">{selectedLabel}</span>
        <span className="shrink-0 text-slate-500">{open ? '▴' : '▾'}</span>
      </button>

      {open && executions.length > 0 && (
        <div className="absolute left-0 right-0 top-full z-30 mt-1 overflow-hidden rounded border border-[#1f2937] bg-[#111827] shadow-xl shadow-black/40">
          <ul className="max-h-64 overflow-y-auto py-1">
            <li>
              <button
                type="button"
                className={cn(
                  'flex w-full flex-col gap-0.5 px-3 py-2 text-left text-sm hover:bg-white/5',
                  !executionId && 'bg-[#3b82f6]/10',
                )}
                onClick={() => {
                  onSelect('')
                  setOpen(false)
                }}
              >
                <span className="font-medium text-slate-100">
                  Latest ({shortExec(latest.replay_execution_id)})
                </span>
                <span className="truncate font-mono text-[11px] text-slate-500">
                  {execSub(latest)}
                </span>
              </button>
            </li>
            {executions.map((e) => {
              const active = executionId === e.replay_execution_id
              return (
                <li key={e.replay_execution_id}>
                  <button
                    type="button"
                    className={cn(
                      'flex w-full flex-col gap-0.5 px-3 py-2 text-left text-sm hover:bg-white/5',
                      active && 'bg-[#3b82f6]/10',
                    )}
                    onClick={() => {
                      onSelect(e.replay_execution_id)
                      setOpen(false)
                    }}
                  >
                    <span className="truncate font-mono text-xs text-slate-100">
                      {e.replay_execution_id}
                    </span>
                    <span className="truncate font-mono text-[11px] text-slate-500">
                      {execSub(e)}
                    </span>
                  </button>
                </li>
              )
            })}
          </ul>
        </div>
      )}
    </div>
  )
}
