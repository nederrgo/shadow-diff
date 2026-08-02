import { useEffect, useMemo, useRef, useState } from 'react'
import type { CatalogEntry } from '@/hooks/useTopologyStream'
import { cn } from '@/lib/cn'

type Props = {
  tests: CatalogEntry[]
  namespace: string
  testName: string
  onSelect: (namespace: string, testName: string) => void
}

export function TestPicker({ tests, namespace, testName, onSelect }: Props) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const rootRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)

  const selectedLabel =
    namespace && testName ? `${namespace}/${testName}` : 'Select a ShadowTest…'

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return tests
    return tests.filter((t) => {
      const full = `${t.namespace}/${t.testName}`.toLowerCase()
      return full.includes(q) || t.namespace.toLowerCase().includes(q) || t.testName.toLowerCase().includes(q)
    })
  }, [tests, query])

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
      // Focus search when the menu opens.
      requestAnimationFrame(() => searchRef.current?.focus())
    }
  }, [open])

  return (
    <div ref={rootRef} className="relative min-w-[16rem] max-w-md flex-1">
      <span className="mb-1 block text-xs text-slate-500">Active ShadowTest</span>
      <button
        type="button"
        className={cn(
          'flex w-full items-center justify-between gap-2 rounded border border-[#1f2937] bg-[#111827] px-3 py-1.5 text-left text-sm',
          'text-slate-100 outline-none hover:border-[#3b82f6]/50 focus:border-[#3b82f6]',
        )}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <span className={cn('truncate', !(namespace && testName) && 'text-slate-500')}>
          {selectedLabel}
        </span>
        <span className="shrink-0 text-slate-500">{open ? '▴' : '▾'}</span>
      </button>

      {open && (
        <div className="absolute left-0 right-0 top-full z-30 mt-1 overflow-hidden rounded border border-[#1f2937] bg-[#111827] shadow-xl shadow-black/40">
          <div className="border-b border-[#1f2937] p-2">
            <input
              ref={searchRef}
              className="w-full rounded border border-[#1f2937] bg-[#090d16] px-2.5 py-1.5 text-sm text-slate-100 outline-none placeholder:text-slate-600 focus:border-[#3b82f6]"
              placeholder="Filter by name or namespace…"
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
                {tests.length === 0 ? 'No running ShadowTests yet.' : 'No matches.'}
              </li>
            )}
            {filtered.map((t) => {
              const active = t.namespace === namespace && t.testName === testName
              return (
                <li key={`${t.namespace}/${t.testName}`}>
                  <button
                    type="button"
                    className={cn(
                      'flex w-full flex-col gap-0.5 px-3 py-2 text-left text-sm hover:bg-white/5',
                      active && 'bg-[#3b82f6]/10',
                    )}
                    onClick={() => {
                      onSelect(t.namespace, t.testName)
                      setOpen(false)
                    }}
                  >
                    <span className="truncate font-medium text-slate-100">
                      <span className="text-slate-500">{t.namespace}/</span>
                      {t.testName}
                    </span>
                    <span className="text-[11px] text-slate-500">
                      {t.phase || '—'}
                      {t.mode ? ` · ${t.mode}` : ''}
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
