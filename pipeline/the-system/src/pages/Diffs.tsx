import { useCallback, useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { DiffsSummary } from '@/components/DiffsSummary'
import { PayloadInspector } from '@/components/PayloadInspector'
import { SessionPicker } from '@/components/SessionPicker'
import { useDiffStream } from '@/hooks/useDiffStream'
import { cn } from '@/lib/cn'
import { tuskHttpBase } from '@/lib/tuskBase'
import type { SessionDiff, ShadowSession, TraceGroup, VerdictFilter } from '@/types/diffs'

const FILTERS: { id: VerdictFilter; label: string }[] = [
  { id: 'ALL', label: 'All' },
  { id: 'MISMATCH', label: 'Regressions' },
  { id: 'VOIDED_BASELINE_DIVERGENCE', label: 'Noise' },
  { id: 'MATCH', label: 'Matches' },
]

function groupTraces(diffs: SessionDiff[]): TraceGroup[] {
  const byTrace = new Map<string, TraceGroup>()
  for (const d of diffs) {
    let g = byTrace.get(d.trace_id)
    if (!g) {
      g = {
        trace_id: d.trace_id,
        method: d.method,
        path: d.path,
        verdict: d.verdict,
        status_code_a: d.status_code_a,
        status_code_b: d.status_code_b,
        status_code_candidate: d.status_code_candidate,
        created_at: d.created_at,
        diffs: [],
      }
      byTrace.set(d.trace_id, g)
    }
    g.diffs.push(d)
    // Prefer non-empty method/path from any row; keep latest created_at ordering via first insert.
    if (!g.method && d.method) g.method = d.method
    if (!g.path && d.path) g.path = d.path
    if (d.verdict) g.verdict = d.verdict
  }
  return Array.from(byTrace.values())
}

function summaryFromDiffs(sessionId: string, diffs: SessionDiff[]) {
  const traces = groupTraces(diffs)
  let match = 0
  let mismatch = 0
  let voided = 0
  for (const t of traces) {
    if (t.verdict === 'MATCH') match++
    else if (t.verdict === 'MISMATCH') mismatch++
    else if (t.verdict === 'VOIDED_BASELINE_DIVERGENCE') voided++
  }
  return { session_id: sessionId, total: traces.length, match, mismatch, voided }
}

export default function Diffs() {
  const [params, setParams] = useSearchParams()
  const sessionId = params.get('session_id') ?? ''

  const [sessions, setSessions] = useState<ShadowSession[]>([])
  const [sessionsError, setSessionsError] = useState<string | null>(null)
  const [diffs, setDiffs] = useState<SessionDiff[]>([])
  const [diffsError, setDiffsError] = useState<string | null>(null)
  const [filter, setFilter] = useState<VerdictFilter>('ALL')
  const [selectedTraceId, setSelectedTraceId] = useState<string | null>(null)
  // Default on: hide boot-only sessions with no projected traces.
  const [withDiffsOnly, setWithDiffsOnly] = useState(true)

  const { summary: liveSummary, lastVerdictTraceId, connectionStatus } = useDiffStream(sessionId)

  const loadSessions = useCallback(async () => {
    try {
      const q = withDiffsOnly ? '?with_diffs=true' : ''
      const resp = await fetch(`${tuskHttpBase()}/api/v1/sessions${q}`)
      if (!resp.ok) {
        setSessionsError(resp.status === 503 ? 'Postgres not configured on Tusk' : `Sessions HTTP ${resp.status}`)
        setSessions([])
        return
      }
      const data = (await resp.json()) as ShadowSession[]
      setSessions(Array.isArray(data) ? data : [])
      setSessionsError(null)
    } catch {
      setSessionsError('Failed to reach Tusk /api')
      setSessions([])
    }
  }, [withDiffsOnly])

  const loadDiffs = useCallback(async (id: string) => {
    if (!id) {
      setDiffs([])
      return
    }
    try {
      const resp = await fetch(`${tuskHttpBase()}/api/v1/diffs?session_id=${encodeURIComponent(id)}`)
      if (!resp.ok) {
        setDiffsError(`Diffs HTTP ${resp.status}`)
        setDiffs([])
        return
      }
      const data = (await resp.json()) as SessionDiff[]
      setDiffs(Array.isArray(data) ? data : [])
      setDiffsError(null)
    } catch {
      setDiffsError('Failed to load diffs')
      setDiffs([])
    }
  }, [])

  useEffect(() => {
    void loadSessions()
  }, [loadSessions])

  useEffect(() => {
    void loadDiffs(sessionId)
    setSelectedTraceId(null)
  }, [sessionId, loadDiffs])

  // Hybrid hydrate: on live verdict, refetch payloads for the session.
  useEffect(() => {
    if (!sessionId || !lastVerdictTraceId) return
    void loadDiffs(sessionId)
  }, [lastVerdictTraceId, sessionId, loadDiffs])

  const selectSession = (id: string) => {
    const next = new URLSearchParams(params)
    if (id) next.set('session_id', id)
    else next.delete('session_id')
    setParams(next, { replace: true })
  }

  const traces = useMemo(() => groupTraces(diffs), [diffs])
  const filtered = useMemo(() => {
    if (filter === 'ALL') return traces
    return traces.filter((t) => t.verdict === filter)
  }, [traces, filter])

  useEffect(() => {
    if (selectedTraceId && filtered.some((t) => t.trace_id === selectedTraceId)) return
    setSelectedTraceId(filtered[0]?.trace_id ?? null)
  }, [filtered, selectedTraceId])

  const summary = liveSummary ?? (sessionId ? summaryFromDiffs(sessionId, diffs) : null)

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex flex-wrap items-end gap-3 border-b border-[#1f2937] bg-[#090d16] px-4 py-3">
        <SessionPicker sessions={sessions} sessionId={sessionId} onSelect={selectSession} />
        <label className="flex items-center gap-2 pb-1.5 text-xs text-slate-400">
          <input
            type="checkbox"
            checked={withDiffsOnly}
            onChange={(e) => setWithDiffsOnly(e.target.checked)}
            className="rounded border-[#1f2937] bg-[#111827] text-[#3b82f6] focus:ring-[#3b82f6]"
          />
          With diffs only
        </label>
        <p className="pb-1.5 text-xs text-slate-500">
          Stream: <code className="text-slate-400">/ws/diffs</code>
          {' · '}
          <span
            className={cn(
              connectionStatus === 'connected' && 'text-emerald-400',
              connectionStatus === 'reconnecting' && 'text-amber-400',
              connectionStatus === 'disconnected' && 'text-slate-500',
            )}
          >
            {connectionStatus}
          </span>
          {` · ${sessions.length} sessions`}
          {sessionsError ? <span className="ml-2 text-red-400">{sessionsError}</span> : null}
          {diffsError ? <span className="ml-2 text-red-400">{diffsError}</span> : null}
        </p>
      </div>

      <div className="flex min-h-0 flex-1 flex-col gap-4 p-4">
        <DiffsSummary summary={sessionId ? summary : null} />

        <div className="flex flex-wrap gap-1">
          {FILTERS.map((f) => (
            <button
              key={f.id}
              type="button"
              onClick={() => setFilter(f.id)}
              className={cn(
                'rounded-md px-3 py-1.5 text-sm font-medium transition-colors',
                filter === f.id
                  ? 'bg-[#3b82f6]/15 text-[#3b82f6]'
                  : 'text-slate-400 hover:bg-white/5 hover:text-slate-200',
              )}
            >
              {f.label}
            </button>
          ))}
        </div>

        {!sessionId ? (
          <div className="flex flex-1 items-center justify-center text-sm text-slate-500">
            Pick a shadow session to inspect diffs.
          </div>
        ) : (
          <PayloadInspector
            traces={filtered}
            selectedTraceId={selectedTraceId}
            onSelectTrace={setSelectedTraceId}
          />
        )}
      </div>
    </div>
  )
}
