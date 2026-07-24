import { useEffect, useState } from 'react';
import BudgetGauge from './components/BudgetGauge';
import HostStrip from './components/HostStrip';
import RequestsTable from './components/RequestsTable';
import Sparkline from './components/Sparkline';
import type { StreamPayload } from './types';

const HISTORY = 120; // seconds of sparkline history

type Conn = 'connecting' | 'live' | 'lost';

const connBadge: Record<Conn, { text: string; cls: string }> = {
  connecting: { text: 'connecting…', cls: 'eyebrow live' },
  live: { text: 'live', cls: 'eyebrow live' },
  lost: { text: 'reconnecting', cls: 'eyebrow lost' },
};

export default function App() {
  const [payload, setPayload] = useState<StreamPayload | null>(null);
  const [conn, setConn] = useState<Conn>('connecting');
  const [tpsHistory, setTpsHistory] = useState<number[]>([]);

  useEffect(() => {
    const es = new EventSource('/api/stream');
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

  return (
    <div className="min-h-screen">
      <header className="nav flex items-center gap-5 px-8 py-3.5">
        <div className="flex items-center gap-2.5">
          <span className="brand-mark">b</span>
          <span className="text-[17px] font-bold tracking-tight">brokemode</span>
        </div>
        <span className="ml-auto flex items-center gap-3">
          <span className={connBadge[conn].cls}>
            <span className="dot" />
            {connBadge[conn].text}
          </span>
          <a
            href="https://github.com/alileza/brokemode"
            className="rounded-lg bg-[var(--content-primary)] px-3 py-1.5 text-sm font-semibold text-white hover:bg-[#1f2630]"
          >
            GitHub →
          </a>
        </span>
      </header>

      <main className="mx-auto max-w-[1100px] px-8 py-10">
        <div className="mb-8">
          <h1 className="text-3xl font-bold tracking-tight">Local LLM dashboard</h1>
          <p className="mt-1 text-[15px] text-[var(--content-secondary)]">
            Live decode rate, memory budget, and host telemetry — billed to your M2.
          </p>
        </div>

        {payload === null ? (
          <p className="py-24 text-center text-[var(--content-secondary)]">
            waiting for /api/stream…
          </p>
        ) : (
          <div className="space-y-4">
            <section className="card">
              <div className="mb-3 flex items-baseline justify-between">
                <h2 className="kicker">Decode rate</h2>
                <span className="text-3xl font-bold tracking-tight tabular-nums">
                  {payload.decode_tps.toFixed(1)}
                  <span className="ml-1 text-sm font-normal text-[var(--content-secondary)]">
                    tok/s
                  </span>
                </span>
              </div>
              <Sparkline values={tpsHistory} unit="tok/s" />
            </section>

            <div className="grid gap-4 md:grid-cols-2">
              <section className="card">
                <h2 className="kicker mb-4">Memory budget</h2>
                <BudgetGauge
                  usedBytes={payload.telemetry.ollama_rss_bytes}
                  budgetGB={payload.budget_gb}
                />
              </section>
              <section className="card">
                <h2 className="kicker mb-4">Last request</h2>
                <div className="text-3xl font-bold tracking-tight tabular-nums">
                  {payload.ttft_ms > 0 ? `${payload.ttft_ms.toFixed(0)} ms` : '—'}
                  <span className="ml-1 text-sm font-normal text-[var(--content-secondary)]">
                    TTFT
                  </span>
                </div>
                <p className="mt-1 text-[13px] text-[var(--content-secondary)]">
                  time to first token, most recent gateway request
                </p>
              </section>
            </div>

            <HostStrip telemetry={payload.telemetry} />

            <section className="card">
              <h2 className="kicker mb-4">Recent gateway requests</h2>
              <RequestsTable requests={payload.recent ?? []} />
            </section>
          </div>
        )}
      </main>

      <footer className="mt-8 border-t border-[var(--border-primary)] bg-[var(--surface-secondary)] px-8 py-6">
        <div className="mx-auto flex max-w-[1100px] items-center gap-6 text-[13px] text-[var(--content-secondary)]">
          <span>brokemode — MIT-licensed open source</span>
          <span className="grow" />
          <a className="hover:text-[var(--content-primary)]" href="/metrics">
            /metrics
          </a>
          <a
            className="hover:text-[var(--content-primary)]"
            href="https://github.com/alileza/brokemode"
          >
            GitHub
          </a>
        </div>
      </footer>
    </div>
  );
}
