import type { RequestRecord } from '../types';

interface RequestsTableProps {
  requests: RequestRecord[];
}

const GB = 1024 ** 3;

/** Recent gateway requests, newest first. */
export default function RequestsTable({ requests }: RequestsTableProps) {
  if (requests.length === 0) {
    return (
      <div>
        <p className="mb-4 text-sm text-[var(--content-secondary)]">
          No gateway requests yet — point Claude Code at the gateway:
        </p>
        <div className="quickstart">
          <span className="prompt">$</span> export ANTHROPIC_BASE_URL=http://127.0.0.1:9100
          {'\n'}
          <span className="prompt">$</span> export ANTHROPIC_AUTH_TOKEN=brokemode-local
          {'\n'}
          <span className="prompt">$</span> claude
        </div>
      </div>
    );
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-sm">
        <thead>
          <tr className="border-b border-[var(--border-primary)]">
            {['Time', 'Model', 'Mode', 'TTFT', 'Decode', 'In', 'Out', 'Peak RSS'].map((h, i) => (
              <th
                key={h}
                className={`py-2.5 pr-4 text-xs font-semibold tracking-wider text-[var(--content-secondary)] uppercase ${
                  i >= 3 ? 'text-right' : ''
                }`}
              >
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="tabular-nums">
          {requests.slice(0, 20).map((r, i) => (
            <tr
              key={`${r.time}-${i}`}
              className="border-b border-[var(--border-primary)] last:border-b-0 hover:bg-[var(--surface-secondary)]"
            >
              <td className="py-2 pr-4 text-[var(--content-secondary)]">
                {new Date(r.time).toLocaleTimeString()}
              </td>
              <td className="py-2 pr-4 font-medium">
                {r.model}
                {r.alias !== undefined && r.alias !== r.model && (
                  <span className="ml-1 text-xs font-normal text-[var(--content-tertiary)]">
                    ← {r.alias}
                  </span>
                )}
              </td>
              <td className="py-2 pr-4 text-[var(--content-secondary)]">
                {r.stream ? 'stream' : 'sync'}
              </td>
              <td className="py-2 pr-4 text-right">{r.ttft_ms.toFixed(0)} ms</td>
              <td className="py-2 pr-4 text-right">{r.decode_tps.toFixed(1)} tok/s</td>
              <td className="py-2 pr-4 text-right">{r.tokens_in}</td>
              <td className="py-2 pr-4 text-right">{r.tokens_out}</td>
              <td className="py-2 text-right">
                {r.peak_rss_bytes > 0 ? `${(r.peak_rss_bytes / GB).toFixed(1)} GB` : '—'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
