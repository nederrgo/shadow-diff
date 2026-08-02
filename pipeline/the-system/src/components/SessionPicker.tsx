import { useEffect, useMemo, useRef, useState } from 'react'
import { cn } from '@/lib/cn'
import type { ShadowSession } from '@/types/diffs'

type Props = {
  sessions: ShadowSession[]
  sessionId: string
  onSelect: (sessionId: string) => void
}

function sessionLabel(s: ShadowSession): string {
  const name = s.shadow_test_name || s.session_id
  const ns = s.namespace ? `${s.namespace}/` : ''
  return `${ns}${name}`
}

function sessionSub(s: ShadowSession): string {
  const parts = [s.session_id]
  if (s.mode) parts.push(s.mode)
  if (s.created_at) {
    try {
      parts.push(new Date(s.created_at).toLocaleString())
    } catch {
      parts.push(s.created_at)
    }
  }
  return parts.join(' · ')
}

export function SessionPicker({ sessions, sessionId, onSelect }: Props) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const rootRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)

  const selected = sessions.find((s) => s.session_id === sessionId)
  const selectedLabel = selected ? sessionLabel(selected) : 'Select a session…'

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return sessions
    return sessions.filter((s) => {
      const hay = [
        s.session_id,
        s.shadow_test_name,
        s.namespace,
        s.mode,
        sessionLabel(s),
      ]
        .join(' ')
        .toLowerCase()
      return hay.includes(q)
    })
  }, [sessions, query])

  useEffect(() => {
    if (!open) return
    const onDoc = (ev: MouseEvent) => {
      if (!rootRef.current?.contains(ev.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [open])

  useEffect(() => {
    if (open) {
      setQuery('')
      requestAnimationFrame(() => searchRef.current?.focus())
    }
  }, [open])

  return (
    <div ref={rootRef} className="relative min-w-[16rem] max-w-md flex-1">
      <span className="mb-1 block text-xs text-slate-500">Active session</span>
      <button
        type="button"
        className={cn(
          'flex w-full items-center justify-between gap-2 rounded border border-[#1f2937] bg-[#111827] px-3 py-1.5 text-left text-sm',
          'text-slate-100 outline-none hover:border-[#3b82f6]/50 focus:border-[#3b82f6]',
        )}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <span className={cn('truncate', !sessionId && 'text-slate-500')}>{selectedLabel}</span>
        <span className="shrink-0 text-slate-500">{open ? '▴' : '▾'}</span>
      </button>

      {open && (
        <div className="absolute left-0 right-0 top-full z-30 mt-1 overflow-hidden rounded border border-[#1f2937] bg-[#111827] shadow-xl shadow-black/40">
          <div className="border-b border-[#1f2937] p-2">
            <input
              ref={searchRef}
              className="w-full rounded border border-[#1f2937] bg-[#090d16] px-2.5 py-1.5 text-sm text-slate-100 outline-none placeholder:text-slate-600 focus:border-[#3b82f6]"
              placeholder="Filter by test, namespace, or session id…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Escape') setOpen(false)
              }}
            />
          </div>
          <ul className="max-h-64 overflow-y-auto py-1">
            {filtered.length === 0 && (
              <li className="px-3 py-2 text-xs text-slate-500">
                {sessions.length === 0 ? 'No sessions yet.' : 'No matches.'}
              </li>
            )}
            {filtered.map((s) => {
              const active = s.session_id === sessionId
              return (
                <li key={s.session_id}>
                  <button
                    type="button"
                    className={cn(
                      'flex w-full flex-col gap-0.5 px-3 py-2 text-left text-sm hover:bg-white/5',
                      active && 'bg-[#3b82f6]/10',
                    )}
                    onClick={() => {
                      onSelect(s.session_id)
                      setOpen(false)
                    }}
                  >
                    <span className="truncate font-medium text-slate-100">
                      {s.namespace ? <span className="text-slate-500">{s.namespace}/</span> : null}
                      {s.shadow_test_name || s.session_id}
                    </span>
                    <span className="truncate font-mono text-[11px] text-slate-500">{sessionSub(s)}</span>
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
