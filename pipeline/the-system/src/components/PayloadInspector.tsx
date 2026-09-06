import { useEffect, useMemo, useRef, useState } from 'react'
import { cn } from '@/lib/cn'
import { tuskHttpBase } from '@/lib/tuskBase'
import type {
  SessionDiff,
  SignatureOccurrence,
  SignatureOccurrences,
  TraceGroup,
  Verdict,
  VerdictStep,
} from '@/types/diffs'

type Props = {
  traces: TraceGroup[]
  selectedTraceId: string | null
  onSelectTrace: (traceId: string) => void
  /** When set, scopes GET /api/v1/diffs/occurrences to this replay run. */
  executionId?: string
}

function verdictBadge(verdict: Verdict) {
  switch (verdict) {
    case 'MATCH':
      return 'bg-emerald-500/15 text-emerald-400'
    case 'MISMATCH':
      return 'bg-red-500/15 text-red-400'
    case 'VOIDED_BASELINE_DIVERGENCE':
      return 'bg-amber-500/15 text-amber-400'
    case 'WAITING_FOR_ROLES':
      return 'bg-slate-500/15 text-slate-400'
    default:
      return 'bg-slate-500/15 text-slate-400'
  }
}

function pretty(value: unknown): string {
  if (value == null) return '—'
  if (typeof value === 'string') {
    try {
      return JSON.stringify(JSON.parse(value), null, 2)
    } catch {
      return value
    }
  }
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

function asSteps(value: unknown): VerdictStep[] {
  if (value == null) return []
  let parsed: unknown = value
  if (typeof value === 'string') {
    try {
      parsed = JSON.parse(value)
    } catch {
      return []
    }
  }
  if (!Array.isArray(parsed)) return []
  return parsed.filter((s): s is VerdictStep => !!s && typeof s === 'object')
}

function parseCountDetail(detail: string | undefined): { candidate: number; controlA: number } | null {
  if (!detail) return null
  const m = detail.match(/candidate=(\d+)\s+control-a=(\d+)/i)
  if (!m) return null
  return { candidate: Number(m[1]), controlA: Number(m[2]) }
}

function stepHeadline(step: VerdictStep): string {
  switch (step.kind) {
    case 'MISMATCH_COUNT':
      if (step.reason === 'UNEXPECTED_EXTRA_EGRESS') return 'Extra occurrences on candidate'
      if (step.reason === 'MISSING_EGRESS') return 'Missing occurrences on candidate'
      return 'Occurrence count differs'
    case 'MISMATCH_SIGNATURE':
      return 'Unexpected signature on candidate'
    case 'MISMATCH_PAYLOAD':
      return step.noise_path ? `Payload field differs: ${step.noise_path}` : 'Payload differs'
    default:
      return step.kind || 'Finding'
  }
}

function PayloadColumn({
  title,
  payload,
  accent,
  subtitle,
}: {
  title: string
  payload: unknown
  accent?: 'noise' | 'regression'
  subtitle?: string
}) {
  return (
    <div
      className={cn(
        'min-w-0 flex-1 rounded-lg border bg-[#0b1220]',
        accent === 'noise' && 'border-amber-500/50',
        accent === 'regression' && 'border-red-500/50',
        !accent && 'border-[#1f2937]',
      )}
    >
      <div className="border-b border-[#1f2937] px-3 py-2">
        <p className="text-xs font-medium uppercase tracking-wide text-slate-400">{title}</p>
        {subtitle ? <p className="mt-0.5 text-[11px] text-slate-500">{subtitle}</p> : null}
      </div>
      <pre className="max-h-[28rem] overflow-auto p-3 text-xs leading-relaxed text-slate-300">
        {pretty(payload)}
      </pre>
    </div>
  )
}

function CountMismatchCard({ step }: { step: VerdictStep }) {
  const counts = parseCountDetail(step.detail)
  const delta = counts ? counts.candidate - counts.controlA : null

  return (
    <div className="rounded-md border border-red-500/50 bg-red-500/10 px-4 py-3 text-sm text-red-100">
      <p className="text-xs font-medium uppercase tracking-wide text-red-300">{stepHeadline(step)}</p>
      <p className="mt-1 font-mono text-xs text-slate-300">{step.signature || '—'}</p>
      {counts ? (
        <div className="mt-3 grid grid-cols-3 gap-2 text-center">
          <div className="rounded-md border border-[#1f2937] bg-[#0b1220] px-2 py-2">
            <p className="text-[10px] uppercase tracking-wide text-slate-500">Control-A</p>
            <p className="mt-1 text-xl font-semibold tabular-nums text-slate-100">{counts.controlA}</p>
          </div>
          <div className="rounded-md border border-[#1f2937] bg-[#0b1220] px-2 py-2">
            <p className="text-[10px] uppercase tracking-wide text-slate-500">Delta</p>
            <p
              className={cn(
                'mt-1 text-xl font-semibold tabular-nums',
                delta != null && delta > 0 && 'text-red-400',
                delta != null && delta < 0 && 'text-amber-400',
                delta === 0 && 'text-slate-100',
              )}
            >
              {delta == null ? '—' : delta > 0 ? `+${delta}` : delta}
            </p>
          </div>
          <div className="rounded-md border border-red-500/40 bg-[#0b1220] px-2 py-2">
            <p className="text-[10px] uppercase tracking-wide text-slate-500">Candidate</p>
            <p className="mt-1 text-xl font-semibold tabular-nums text-red-400">{counts.candidate}</p>
          </div>
        </div>
      ) : (
        <p className="mt-2 font-mono text-xs text-red-200">{step.detail || step.reason || ''}</p>
      )}
      <p className="mt-3 text-xs text-slate-400">
        Count regressions compare how many times this signature ran per role. Use the occurrence chips below to
        inspect each index; missing sides show as empty.
      </p>
    </div>
  )
}

function PayloadStepCard({ step, active }: { step: VerdictStep; active?: boolean }) {
  return (
    <div
      className={cn(
        'rounded-md border px-3 py-2 text-xs text-red-200',
        active ? 'border-red-400 bg-red-500/20' : 'border-red-500/40 bg-red-500/10',
      )}
    >
      <p className="font-medium uppercase tracking-wide text-red-300">{stepHeadline(step)}</p>
      {step.detail ? <p className="mt-1 font-mono text-red-100/90">{step.detail}</p> : null}
      {step.reason ? <p className="mt-1 text-slate-400">reason: {step.reason}</p> : null}
    </div>
  )
}

function NoiseBanner({ value }: { value: unknown }) {
  if (value == null) return null
  return (
    <div className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
      <p className="mb-1 font-medium uppercase tracking-wide">Noise (control-a vs control-b)</p>
      <pre className="overflow-auto whitespace-pre-wrap">{pretty(value)}</pre>
    </div>
  )
}

function cacheKey(traceId: string, signature: string, execId: string) {
  return `${execId}\0${traceId}\0${signature}`
}

function firstRedIndex(mismatchIndexes: Set<number>): number {
  if (mismatchIndexes.size === 0) return 0
  return Math.min(...mismatchIndexes)
}

export function PayloadInspector({ traces, selectedTraceId, onSelectTrace, executionId = '' }: Props) {
  const selected = traces.find((t) => t.trace_id === selectedTraceId) ?? null
  const [sigIndex, setSigIndex] = useState(0)
  const [occIndex, setOccIndex] = useState(0)
  const [occCache, setOccCache] = useState<Record<string, SignatureOccurrences>>({})
  const [occLoading, setOccLoading] = useState(false)
  const [occError, setOccError] = useState<string | null>(null)

  const activeDiff: SessionDiff | null = useMemo(() => {
    if (!selected || selected.diffs.length === 0) return null
    return selected.diffs[Math.min(sigIndex, selected.diffs.length - 1)] ?? null
  }, [selected, sigIndex])

  const steps = useMemo(() => asSteps(activeDiff?.regression_diff), [activeDiff])
  const countSteps = useMemo(
    () => steps.filter((s) => s.kind === 'MISMATCH_COUNT' || s.kind === 'MISMATCH_SIGNATURE'),
    [steps],
  )
  const payloadSteps = useMemo(() => steps.filter((s) => s.kind === 'MISMATCH_PAYLOAD'), [steps])
  const mismatchIndexes = useMemo(() => {
    const set = new Set<number>()
    for (const s of payloadSteps) {
      if (typeof s.index === 'number') set.add(s.index)
    }
    return set
  }, [payloadSteps])

  const activeKey = activeDiff
    ? cacheKey(activeDiff.trace_id, activeDiff.signature, executionId)
    : null
  const loaded = activeKey ? occCache[activeKey] : undefined
  const occCacheRef = useRef(occCache)
  occCacheRef.current = occCache

  useEffect(() => {
    setSigIndex(0)
  }, [selectedTraceId])

  useEffect(() => {
    const red = new Set<number>()
    for (const s of asSteps(activeDiff?.regression_diff)) {
      if (s.kind === 'MISMATCH_PAYLOAD' && typeof s.index === 'number') red.add(s.index)
    }
    setOccIndex(firstRedIndex(red))
  }, [activeKey, activeDiff?.regression_diff])

  useEffect(() => {
    if (!activeDiff || !activeKey) return
    if (occCacheRef.current[activeKey]) {
      setOccLoading(false)
      setOccError(null)
      return
    }

    const ac = new AbortController()
    const key = activeKey
    const traceID = activeDiff.trace_id
    const signature = activeDiff.signature
    setOccLoading(true)
    setOccError(null)
    const qs = new URLSearchParams({
      trace_id: traceID,
      signature,
    })
    if (executionId) qs.set('replay_execution_id', executionId)
    const url = `${tuskHttpBase()}/api/v1/diffs/occurrences?${qs}`

    fetch(url, { signal: ac.signal })
      .then(async (res) => {
        if (!res.ok) throw new Error(`Occurrences HTTP ${res.status}`)
        return (await res.json()) as SignatureOccurrences
      })
      .then((data) => {
        setOccCache((prev) => ({ ...prev, [key]: data }))
      })
      .catch((err: unknown) => {
        if (ac.signal.aborted) return
        setOccError(err instanceof Error ? err.message : 'Failed to load occurrences')
      })
      .finally(() => {
        if (!ac.signal.aborted) setOccLoading(false)
      })

    return () => ac.abort()
  }, [activeDiff?.trace_id, activeDiff?.signature, activeKey, executionId])

  const occurrences: SignatureOccurrence[] = useMemo(() => {
    if (loaded?.occurrences?.length) return loaded.occurrences
    if (!activeDiff) return []
    // Optimistic first occurrence from session hydrate until lazy fetch returns.
    return [
      {
        index: 0,
        control_a_payload: activeDiff.control_a_payload,
        control_b_payload: activeDiff.control_b_payload,
        candidate_payload: activeDiff.candidate_payload,
      },
    ]
  }, [loaded, activeDiff])

  const selectedOcc = occurrences[Math.min(occIndex, Math.max(occurrences.length - 1, 0))] ?? null
  const selectedIsMismatch = selectedOcc != null && mismatchIndexes.has(selectedOcc.index)

  return (
    <div className="flex min-h-0 flex-1 gap-3">
      <aside className="flex w-72 shrink-0 flex-col overflow-hidden rounded-lg border border-[#1f2937] bg-[#111827]">
        <div className="border-b border-[#1f2937] px-3 py-2 text-xs uppercase tracking-wide text-slate-500">
          Traces ({traces.length})
        </div>
        <ul className="min-h-0 flex-1 overflow-y-auto">
          {traces.length === 0 ? (
            <li className="px-3 py-6 text-center text-sm text-slate-500">No traces for this filter</li>
          ) : (
            traces.map((t) => (
              <li key={t.trace_id}>
                <button
                  type="button"
                  onClick={() => onSelectTrace(t.trace_id)}
                  className={cn(
                    'flex w-full flex-col gap-1 border-b border-[#1f2937] px-3 py-2.5 text-left transition-colors',
                    selectedTraceId === t.trace_id ? 'bg-[#3b82f6]/10' : 'hover:bg-white/5',
                  )}
                >
                  <span className="truncate font-mono text-xs text-slate-200">{t.trace_id}</span>
                  <span className="truncate text-xs text-slate-500">
                    {t.method || '—'} {t.path || ''}
                  </span>
                  <span
                    className={cn(
                      'inline-flex w-fit rounded px-1.5 py-0.5 text-[10px] font-medium uppercase',
                      verdictBadge(t.verdict),
                    )}
                  >
                    {t.verdict || 'UNKNOWN'}
                  </span>
                </button>
              </li>
            ))
          )}
        </ul>
      </aside>

      <section className="flex min-w-0 flex-1 flex-col gap-3 overflow-hidden">
        {!selected || !activeDiff || !selectedOcc ? (
          <div className="flex flex-1 items-center justify-center rounded-lg border border-[#1f2937] bg-[#111827] text-sm text-slate-500">
            Select a trace to inspect payloads
          </div>
        ) : (
          <>
            {selected.diffs.length > 1 ? (
              <div className="flex flex-wrap gap-1">
                {selected.diffs.map((d, i) => (
                  <button
                    key={d.signature}
                    type="button"
                    onClick={() => setSigIndex(i)}
                    className={cn(
                      'rounded-md px-2.5 py-1 font-mono text-xs transition-colors',
                      i === sigIndex
                        ? 'bg-[#3b82f6]/20 text-[#3b82f6]'
                        : 'bg-[#111827] text-slate-400 hover:bg-white/5',
                    )}
                  >
                    {d.signature}
                  </button>
                ))}
              </div>
            ) : (
              <p className="font-mono text-xs text-slate-500">{activeDiff.signature}</p>
            )}

            <div className="flex flex-wrap items-center gap-1">
              <span className="mr-1 text-[11px] uppercase tracking-wide text-slate-500">Occurrence</span>
              {occurrences.map((o) => {
                const red = mismatchIndexes.has(o.index)
                const active = o.index === occIndex
                return (
                  <button
                    key={o.index}
                    type="button"
                    onClick={() => setOccIndex(o.index)}
                    className={cn(
                      'min-w-8 rounded-md px-2.5 py-1 text-xs font-medium tabular-nums transition-colors',
                      red && active && 'bg-red-500/30 text-red-200 ring-1 ring-red-400/60',
                      red && !active && 'bg-red-500/15 text-red-300 hover:bg-red-500/25',
                      !red && active && 'bg-[#3b82f6]/20 text-[#3b82f6]',
                      !red && !active && 'bg-[#111827] text-slate-400 hover:bg-white/5',
                    )}
                  >
                    {o.index + 1}
                  </button>
                )
              })}
              {occLoading ? <span className="ml-2 text-[11px] text-slate-500">Loading…</span> : null}
              {occError ? <span className="ml-2 text-[11px] text-red-400">{occError}</span> : null}
              {loaded?.truncated ? (
                <span className="ml-2 text-[11px] text-amber-400">Showing first 50</span>
              ) : null}
            </div>

            <div className="flex max-h-[40%] flex-col gap-2 overflow-y-auto">
              <NoiseBanner value={activeDiff.noise_diff} />
              {countSteps.map((step, i) => (
                <CountMismatchCard key={`count-${i}`} step={step} />
              ))}
              {payloadSteps.map((step, i) => (
                <PayloadStepCard
                  key={`payload-${i}`}
                  step={step}
                  active={typeof step.index === 'number' && step.index === occIndex}
                />
              ))}
              {steps.length === 0 && activeDiff.regression_diff != null ? (
                <div className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-200">
                  <p className="mb-1 font-medium uppercase tracking-wide">Regression</p>
                  <pre className="overflow-auto whitespace-pre-wrap">{pretty(activeDiff.regression_diff)}</pre>
                </div>
              ) : null}
            </div>

            <div className="flex min-h-0 flex-1 gap-2 overflow-hidden">
              <PayloadColumn
                title="Control-A"
                payload={selectedOcc.control_a_payload}
                accent={activeDiff.noise_diff != null ? 'noise' : undefined}
                subtitle={`Occurrence ${selectedOcc.index + 1}`}
              />
              <PayloadColumn
                title="Control-B"
                payload={selectedOcc.control_b_payload}
                accent={activeDiff.noise_diff != null ? 'noise' : undefined}
                subtitle={`Occurrence ${selectedOcc.index + 1}`}
              />
              <PayloadColumn
                title="Candidate"
                payload={selectedOcc.candidate_payload}
                accent={selectedIsMismatch ? 'regression' : undefined}
                subtitle={`Occurrence ${selectedOcc.index + 1}`}
              />
            </div>
          </>
        )}
      </section>
    </div>
  )
}
