import { useEffect, useRef, useState } from 'react';
import BudgetGauge from './components/BudgetGauge';
import HostStrip from './components/HostStrip';
import RequestsTable from './components/RequestsTable';
import Sparkline from './components/Sparkline';
import type { StreamPayload } from './types';

const HISTORY = 120; // seconds of sparkline history

type Conn = 'connecting' | 'live' | 'lost';

export default function App() {
  const [payload, setPayload] = useState<StreamPayload | null>(null);
  const [conn, setConn] = useState<Conn>('connecting');
  const [tpsHistory, setTpsHistory] = useState<number[]>([]);
  const esRef = useRef<EventSource | null>(null);

  useEffect(() => {
    const es = new EventSource('/api/stream');
    esRef.current = es;
    es.addEventListener('telemetry', (ev: MessageEvent<string>) => {
      const data: StreamPayload = JSON.parse(ev.data) as StreamPayload;
      setPayload(data);
      setConn('live');
      setTpsHistory((h) => [...h, data.decode_tps].slice(-HISTORY));
    });
    es.onerror = () => {
      setConn('lost');
    };
    return () => {
      es.close();
    };
  }, []);

  const connBadge: Record<Conn, { text: string; cls: string }> = {
    connecting: { text: 'connecting…', cls: 'text-slate-400' },
    live: { text: '● live', cls: 'text-emerald-500' },
    lost: { text: '○ reconnecting', cls: 'text-amber-500' },
  };

  return (
    <div className="mx-auto max-w-5xl px-6 py-8">
      <header className="mb-6 flex items-baseline justify-between">
        <div>
          <h1 className="text-2xl font-bold text-slate-100">brokemode</h1>
          <p className="text-sm text-slate-500">local LLMs on Apple Silicon — zero token cost</p>
        </div>
        <span className={`text-sm ${connBadge[conn].cls}`}>{connBadge[conn].text}</span>
      </header>

      {payload === null ? (
        <p className="py-24 text-center text-slate-500">waiting for /api/stream…</p>
      ) : (
        <div className="space-y-4">
          <section className="rounded-xl border border-slate-800 bg-slate-950/40 p-5">
            <div className="mb-2 flex items-baseline justify-between">
              <h2 className="text-sm font-medium uppercase tracking-wide text-slate-500">
                Decode rate
              </h2>
              <span className="text-3xl font-semibold tabular-nums text-slate-100">
                {payload.decode_tps.toFixed(1)}
                <span className="ml-1 text-sm font-normal text-slate-400">tok/s</span>
              </span>
            </div>
            <Sparkline values={tpsHistory} unit="tok/s" />
          </section>

          <div className="grid gap-4 md:grid-cols-2">
            <section className="rounded-xl border border-slate-800 bg-slate-950/40 p-5">
              <h2 className="mb-3 text-sm font-medium uppercase tracking-wide text-slate-500">
                Memory budget
              </h2>
              <BudgetGauge
                usedBytes={payload.telemetry.ollama_rss_bytes}
                budgetGB={payload.budget_gb}
              />
            </section>
            <section className="rounded-xl border border-slate-800 bg-slate-950/40 p-5">
              <h2 className="mb-3 text-sm font-medium uppercase tracking-wide text-slate-500">
                Last request
              </h2>
              <div className="text-3xl font-semibold tabular-nums text-slate-100">
                {payload.ttft_ms > 0 ? `${payload.ttft_ms.toFixed(0)} ms` : '—'}
                <span className="ml-1 text-sm font-normal text-slate-400">TTFT</span>
              </div>
              <p className="mt-1 text-xs text-slate-500">
                time to first token, most recent gateway request
              </p>
            </section>
          </div>

          <HostStrip telemetry={payload.telemetry} />

          <section className="rounded-xl border border-slate-800 bg-slate-950/40 p-5">
            <h2 className="mb-3 text-sm font-medium uppercase tracking-wide text-slate-500">
              Recent gateway requests
            </h2>
            <RequestsTable requests={payload.recent ?? []} />
          </section>
        </div>
      )}
    </div>
  );
}
