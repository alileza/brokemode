import type { TelemetrySnapshot } from '../types';

interface HostStripProps {
  telemetry: TelemetrySnapshot;
}

const GB = 1024 ** 3;

interface TileProps {
  label: string;
  value: string;
  sub?: string;
  tone?: 'ok' | 'warn' | 'bad';
}

const toneColor: Record<NonNullable<TileProps['tone']>, string> = {
  ok: '#059669',
  warn: '#d97706',
  bad: '#ef4444',
};

function Tile({ label, value, sub, tone }: TileProps) {
  return (
    <div className="rounded-lg border border-slate-800 bg-slate-950/40 px-4 py-3">
      <div className="text-xs uppercase tracking-wide text-slate-500">{label}</div>
      <div className="mt-1 flex items-baseline gap-2">
        <span className="text-xl font-semibold tabular-nums text-slate-100">{value}</span>
        {tone !== undefined && (
          <span
            className="inline-block h-2 w-2 rounded-full"
            style={{ backgroundColor: toneColor[tone] }}
            aria-hidden="true"
          />
        )}
      </div>
      {sub !== undefined && <div className="mt-0.5 text-xs text-slate-500">{sub}</div>}
    </div>
  );
}

/** GPU residency / power / memory pressure / thermal state, 1Hz. */
export default function HostStrip({ telemetry: t }: HostStripProps) {
  const pressureTone =
    t.memory_pressure_pct >= 80 ? 'bad' : t.memory_pressure_pct >= 60 ? 'warn' : 'ok';
  const thermalTone = t.thermal_level > 0 ? 'bad' : 'ok';

  return (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
      <Tile
        label="GPU active"
        value={`${(t.gpu_active_ratio * 100).toFixed(0)}%`}
        sub={
          t.package_power_w > 0
            ? `${t.package_power_w.toFixed(1)} W package`
            : 'powermetrics needs sudo'
        }
      />
      <Tile
        label="Memory pressure"
        value={`${t.memory_pressure_pct.toFixed(0)}%`}
        tone={pressureTone}
        sub={pressureTone === 'ok' ? 'nominal' : pressureTone === 'warn' ? 'elevated' : 'critical'}
      />
      <Tile
        label="Wired + compressed"
        value={`${((t.wired_bytes + t.compressed_bytes) / GB).toFixed(1)} GB`}
        sub={`compressed ${(t.compressed_bytes / GB).toFixed(1)} GB`}
      />
      <Tile
        label="Thermal"
        value={t.thermal_level > 0 ? `throttled ${t.cpu_speed_limit}%` : 'nominal'}
        tone={thermalTone}
        sub={t.thermal_level > 0 ? 'CPU speed limited' : 'no speed limit'}
      />
    </div>
  );
}
