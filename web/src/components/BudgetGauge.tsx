interface BudgetGaugeProps {
  usedBytes: number;
  budgetGB: number;
}

const GB = 1024 ** 3;

function statusOf(ratio: number): { color: string; label: string } {
  if (ratio >= 0.92) return { color: 'var(--status-bad)', label: 'over budget soon' };
  if (ratio >= 0.75) return { color: 'var(--status-warn)', label: 'getting tight' };
  return { color: 'var(--status-good)', label: 'healthy' };
}

/** Ollama RSS as a bar against the max_rss_gb budget, label always visible. */
export default function BudgetGauge({ usedBytes, budgetGB }: BudgetGaugeProps) {
  const usedGB = usedBytes / GB;
  const ratio = budgetGB > 0 ? Math.min(1, usedGB / budgetGB) : 0;
  const { color, label } = statusOf(ratio);

  return (
    <div>
      <div className="flex items-baseline justify-between">
        <span className="text-3xl font-bold tracking-tight tabular-nums">
          {usedGB.toFixed(1)}
          <span className="ml-1 text-sm font-normal text-[var(--content-secondary)]">
            / {budgetGB.toFixed(0)} GB
          </span>
        </span>
        <span className="text-xs text-[var(--content-secondary)]">
          {(ratio * 100).toFixed(0)}% · {label}
        </span>
      </div>
      <div
        className="mt-3 h-3 w-full overflow-hidden rounded-full bg-[var(--surface-secondary)]"
        role="meter"
        aria-valuemin={0}
        aria-valuemax={budgetGB}
        aria-valuenow={Number(usedGB.toFixed(1))}
        aria-label="ollama resident memory against budget"
      >
        <div
          className="h-full rounded-full transition-all duration-500"
          style={{ width: `${ratio * 100}%`, backgroundColor: color }}
        />
      </div>
    </div>
  );
}
