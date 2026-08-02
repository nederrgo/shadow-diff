import { cn } from '@/lib/cn'
import type { DiffSummary } from '@/types/diffs'

type Props = {
  summary: DiffSummary | null
}

const cards = [
  { key: 'total' as const, label: 'Total Traces', className: 'text-slate-100' },
  { key: 'match' as const, label: 'Matches', className: 'text-emerald-400' },
  { key: 'mismatch' as const, label: 'Regressions', className: 'text-red-400' },
  { key: 'voided' as const, label: 'Baseline Noise', className: 'text-amber-400' },
]

export function DiffsSummary({ summary }: Props) {
  const values = summary ?? { total: 0, match: 0, mismatch: 0, voided: 0 }

  return (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
      {cards.map((card) => (
        <div
          key={card.key}
          className="rounded-lg border border-[#1f2937] bg-[#111827] px-4 py-3"
        >
          <p className="text-xs uppercase tracking-wide text-slate-500">{card.label}</p>
          <p className={cn('mt-1 text-2xl font-semibold tabular-nums', card.className)}>
            {values[card.key]}
          </p>
        </div>
      ))}
    </div>
  )
}
