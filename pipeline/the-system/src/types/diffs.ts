export type Verdict =
  | 'MATCH'
  | 'MISMATCH'
  | 'VOIDED_BASELINE_DIVERGENCE'
  | 'WAITING_FOR_ROLES'
  | string

export type VerdictFilter = 'ALL' | 'MISMATCH' | 'VOIDED_BASELINE_DIVERGENCE' | 'MATCH'

export type ShadowSession = {
  session_id: string
  shadow_test_name: string
  namespace: string
  mode: string
  created_at: string
}

export type ReplayExecution = {
  replay_execution_id: string
  session_id: string
  created_at: string
}

export type SessionDiff = {
  trace_id: string
  session_id: string
  replay_execution_id?: string
  signature: string
  source_type: string
  method: string
  path: string
  status_code_a: string
  status_code_b: string
  status_code_candidate: string
  control_a_payload: unknown
  control_b_payload: unknown
  candidate_payload: unknown
  noise_diff: unknown
  regression_diff: unknown
  verdict: Verdict
  created_at: string
}

export type DiffSummary = {
  session_id: string
  replay_execution_id?: string
  total: number
  match: number
  mismatch: number
  voided: number
}

export type DiffWsFrame =
  | ({ type: 'summary' } & DiffSummary)
  | {
      type: 'verdict'
      session_id: string
      replay_execution_id?: string
      trace_id: string
      verdict: Verdict
    }

export type TraceGroup = {
  trace_id: string
  method: string
  path: string
  verdict: Verdict
  status_code_a: string
  status_code_b: string
  status_code_candidate: string
  created_at: string
  diffs: SessionDiff[]
}

/** One step from Beru's VerdictDetails / diff_reports.regression_diff. */
export type VerdictStep = {
  kind: string
  reason?: string
  protocol?: string
  signature?: string
  index?: number
  detail?: string
  noise_path?: string
}

/** One aligned index from Tusk GET /api/v1/diffs/occurrences. */
export type SignatureOccurrence = {
  index: number
  control_a_payload: unknown
  control_b_payload: unknown
  candidate_payload: unknown
}

export type SignatureOccurrences = {
  trace_id: string
  signature: string
  replay_execution_id?: string
  occurrences: SignatureOccurrence[]
  truncated: boolean
}
