import type { RequestRecord } from '../types';

interface RequestsTableProps {
  requests: RequestRecord[];
}

const GB = 1024 ** 3;

/** Recent gateway requests, newest first. */
export default function RequestsTable({ requests }: RequestsTableProps) {
  if (requests.length === 0) {
    return (
      <p className="py-6 text-center text-sm text-slate-500">
        No gateway requests yet — point Claude Code at{' '}
        <code className="rounded bg-slate-800 px-1">ANTHROPIC_BASE_URL=http://127.0.0.1:9100</code>
      </p>
    );
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-sm">
        <thead>
          <tr className="border-b border-slate-800 text-xs uppercase tracking-wide text-slate-500">
            <th className="py-2 pr-4 font-medium">Time</th>
            <th className="py-2 pr-4 font-medium">Model</th>
            <th className="py-2 pr-4 font-medium">Mode</th>
            <th className="py-2 pr-4 text-right font-medium">TTFT</th>
            <th className="py-2 pr-4 text-right font-medium">Decode</th>
            <th className="py-2 pr-4 text-right font-medium">In</th>
            <th className="py-2 pr-4 text-right font-medium">Out</th>
            <th className="py-2 text-right font-medium">Peak RSS</th>
          </tr>
        </thead>
        <tbody className="tabular-nums">
          {requests.slice(0, 20).map((r, i) => (
            <tr
              key={`${r.time}-${i}`}
              className="border-b border-slate-800/50 hover:bg-slate-800/30"
            >
              <td className="py-1.5 pr-4 text-slate-400">
                {new Date(r.time).toLocaleTimeString()}
              </td>
              <td className="py-1.5 pr-4">
                {r.model}
                {r.alias !== undefined && r.alias !== r.model && (
                  <span className="ml-1 text-xs text-slate-500">← {r.alias}</span>
                )}
              </td>
              <td className="py-1.5 pr-4 text-slate-400">{r.stream ? 'stream' : 'sync'}</td>
              <td className="py-1.5 pr-4 text-right">{r.ttft_ms.toFixed(0)} ms</td>
              <td className="py-1.5 pr-4 text-right">{r.decode_tps.toFixed(1)} tok/s</td>
              <td className="py-1.5 pr-4 text-right">{r.tokens_in}</td>
              <td className="py-1.5 pr-4 text-right">{r.tokens_out}</td>
              <td className="py-1.5 text-right">
                {r.peak_rss_bytes > 0 ? `${(r.peak_rss_bytes / GB).toFixed(1)} GB` : '—'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
