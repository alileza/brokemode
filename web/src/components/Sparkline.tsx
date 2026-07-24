import { useId, useState } from 'react';

interface SparklineProps {
  values: number[];
  width?: number;
  height?: number;
  stroke?: string;
  unit: string;
}

/** Single-series live sparkline with a hover crosshair + value readout. */
export default function Sparkline({
  values,
  width = 560,
  height = 96,
  stroke = '#0057ff',
  unit,
}: SparklineProps) {
  const id = useId();
  const [hover, setHover] = useState<number | null>(null);
  const pad = 4;
  const max = Math.max(1, ...values);
  const step = values.length > 1 ? (width - pad * 2) / (values.length - 1) : 0;

  const x = (i: number): number => pad + i * step;
  const y = (v: number): number => height - pad - (v / max) * (height - pad * 2);
  const points = values.map((v, i) => `${x(i)},${y(v)}`).join(' ');

  const hoverIndex =
    hover === null || values.length < 2
      ? null
      : Math.min(values.length - 1, Math.max(0, Math.round((hover - pad) / step)));
  const hoverValue = hoverIndex === null ? null : values[hoverIndex];

  return (
    <svg
      width="100%"
      viewBox={`0 0 ${width} ${height}`}
      role="img"
      aria-label={`decode rate history, currently ${values.at(-1)?.toFixed(1) ?? '0'} ${unit}`}
      onMouseMove={(e) => {
        const rect = e.currentTarget.getBoundingClientRect();
        setHover(((e.clientX - rect.left) / rect.width) * width);
      }}
      onMouseLeave={() => {
        setHover(null);
      }}
    >
      <defs>
        <linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={stroke} stopOpacity="0.18" />
          <stop offset="100%" stopColor={stroke} stopOpacity="0" />
        </linearGradient>
      </defs>
      {values.length > 1 && (
        <>
          <polygon
            points={`${pad},${height - pad} ${points} ${x(values.length - 1)},${height - pad}`}
            fill={`url(#${id})`}
          />
          <polyline points={points} fill="none" stroke={stroke} strokeWidth="2" />
        </>
      )}
      {hoverIndex !== null && hoverValue !== undefined && hoverValue !== null && (
        <g>
          <line
            x1={x(hoverIndex)}
            y1={pad}
            x2={x(hoverIndex)}
            y2={height - pad}
            stroke="#9ca4ad"
            strokeWidth="1"
          />
          <circle
            cx={x(hoverIndex)}
            cy={y(hoverValue)}
            r="4"
            fill={stroke}
            stroke="#ffffff"
            strokeWidth="2"
          />
          <text
            x={x(hoverIndex) + (hoverIndex > values.length / 2 ? -8 : 8)}
            y={pad + 12}
            textAnchor={hoverIndex > values.length / 2 ? 'end' : 'start'}
            fill="#0a0d12"
            fontSize="11"
          >
            {hoverValue.toFixed(1)} {unit}
          </text>
        </g>
      )}
    </svg>
  );
}
