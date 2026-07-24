interface BudgetGaugeProps {
  usedBytes: number;
  budgetGB: number;
}

const GB = 1024 ** 3;

function statusOf(ratio: number): { color: string; label: string } {
  if (ratio >= 0.92) return { color: '#ef4444', label: 'over budget soon' };
  if (ratio >= 0.75) return { color: '#d97706', label: 'getting tight' };
  return { color: '#059669', label: 'healthy' };
}

/** Ollama RSS as a bar against the max_rss_gb budget, label always visible. */
export default function BudgetGauge({ usedBytes, budgetGB }: BudgetGaugeProps) {
  const usedGB = usedBytes / GB;
  const ratio = budgetGB > 0 ? Math.min(1, usedGB / budgetGB) : 0;
  const { color, label } = statusOf(ratio);

  return (
    <div>
      <div className="flex items-baseline justify-between">
        <span className="text-3xl font-semibold tabular-nums text-slate-100">
          {usedGB.toFixed(1)}
          <span className="ml-1 text-sm font-normal text-slate-400">
            / {budgetGB.toFixed(0)} GB
          </span>
        </span>
        <span className="text-xs text-slate-400">
          {(ratio * 100).toFixed(0)}% · {label}
        </span>
      </div>
      <div
        className="mt-2 h-3 w-full overflow-hidden rounded bg-slate-800"
        role="meter"
        aria-valuemin={0}
        aria-valuemax={budgetGB}
        aria-valuenow={Number(usedGB.toFixed(1))}
        aria-label="ollama resident memory against budget"
      >
        <div
          className="h-full rounded transition-all duration-500"
          style={{ width: `${ratio * 100}%`, backgroundColor: color }}
        />
      </div>
    </div>
  );
}
