import { cn } from '@/lib/cn'

export type TeardownState = 'deleting' | 'deleted' | null

type Props = {
  state: TeardownState
  namespace?: string
  testName?: string
}

/**
 * System-style notification strip above the topology canvas (not overlaid on
 * React Flow — absolute siblings get painted under the canvas pane).
 */
export function TeardownBanner({ state, namespace, testName }: Props) {
  if (!state) return null

  const identity = [namespace, testName].filter(Boolean).join('/') || 'ShadowTest'
  const headline = state === 'deleting' ? 'DELETING SHADOWTEST' : 'SHADOWTEST DELETED'
  const body =
    state === 'deleting'
      ? 'Tearing down the shadow stack. Bring-up is blocked until removal finishes.'
      : 'Finalizers cleared. This test is gone from the cluster.'

  return (
    <div
      className={cn(
        'shrink-0 border-b border-red-500/40 bg-[#0a0608] px-4 py-3',
        'shadow-[inset_0_0_40px_rgba(239,68,68,0.12)]',
      )}
      role="status"
      aria-live="polite"
    >
      <div
        className={cn(
          'mx-auto max-w-4xl border border-red-400/70 bg-black/40 px-4 py-3',
          'shadow-[0_0_18px_rgba(239,68,68,0.45),inset_0_0_24px_rgba(239,68,68,0.08)]',
        )}
      >
        <div className="mb-2.5 flex items-center gap-2">
          <span
            className={cn(
              'inline-flex h-7 w-7 shrink-0 items-center justify-center border border-red-400/80',
              'text-sm font-bold text-red-300',
              'shadow-[0_0_10px_rgba(248,113,113,0.7)]',
            )}
          >
            <span className="inline-flex h-4 w-4 items-center justify-center rounded-full border border-red-300 text-[10px] leading-none">
              !
            </span>
          </span>
          <span
            className={cn(
              'border border-red-400/80 px-3 py-1 text-[11px] font-bold tracking-[0.22em] text-red-200',
              'shadow-[0_0_10px_rgba(248,113,113,0.55)]',
            )}
          >
            NOTIFICATION
          </span>
        </div>

        <p
          className={cn(
            'text-sm font-semibold tracking-wide text-red-100',
            'drop-shadow-[0_0_8px_rgba(248,113,113,0.85)]',
          )}
        >
          {headline}
        </p>
        <p className="mt-1 text-sm text-red-100/95 drop-shadow-[0_0_6px_rgba(248,113,113,0.55)]">
          <span className="font-semibold text-red-400">{identity}</span>
          <span className="text-red-200/80"> — {body}</span>
        </p>
      </div>
    </div>
  )
}
