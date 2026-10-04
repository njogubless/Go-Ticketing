import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
  type TooltipProps,
} from 'recharts';
import { api } from '@/api/client';
import type { Stats } from '@/api/types';
import { Card, EmptyState, ErrorMessage, Spinner } from '@/components/primitives';
import { duration, percent } from '@/lib/format';

/**
 * The service-management dashboard.
 *
 * Chart decisions, in the order they were made:
 *
 *  1. Form first. Most of what a service manager wants is a single number —
 *     "are we hitting our targets, and what is on fire?" — so the headline
 *     figures are stat tiles, not charts. Only two things here genuinely need
 *     a plot: change over time, and a comparison across priorities.
 *
 *  2. Colour by job. The volume chart has two unrelated series (raised vs
 *     resolved) so it takes categorical slots 1 and 2. The priority chart is
 *     an *ordered* severity, not a set of unrelated categories, so it takes a
 *     single-hue ordinal ramp — darker means more severe, which is information
 *     rather than decoration. Both palettes were run through the validator in
 *     light and dark against this app's own surfaces.
 *
 *  3. One axis, always. Raised and resolved share a unit (tickets), so they
 *     share a scale. Attainment percentages are on separate tiles rather than
 *     squeezed onto a second y-axis.
 */
export function ReportsPage(): JSX.Element {
  const [days, setDays] = useState(30);
  const [showTable, setShowTable] = useState(false);

  const from = new Date(Date.now() - days * 24 * 60 * 60 * 1000).toISOString();
  const statsQuery = useQuery({
    queryKey: ['reports', 'overview', days],
    queryFn: () => api.reportOverview({ from }),
  });

  if (statsQuery.isLoading) {
    return (
      <div className="main__body">
        <Spinner label="Loading reports" />
      </div>
    );
  }
  if (statsQuery.isError || !statsQuery.data) {
    return (
      <div className="main__body">
        <ErrorMessage error={statsQuery.error} />
      </div>
    );
  }

  const stats = statsQuery.data;

  return (
    <>
      <header className="main__header">
        <div>
          <h1>Reports</h1>
          <div className="subtle">Service desk performance</div>
        </div>
        {/* Filters sit in one row above the charts, per the interaction spec. */}
        <div className="row">
          {[7, 30, 90].map((option) => (
            <button
              key={option}
              type="button"
              className={`button button--sm${days === option ? ' button--primary' : ''}`}
              onClick={() => setDays(option)}
            >
              {option}d
            </button>
          ))}
          <button
            type="button"
            className="button button--sm"
            onClick={() => setShowTable(!showTable)}
            aria-pressed={showTable}
          >
            {showTable ? 'Show charts' : 'Show data'}
          </button>
        </div>
      </header>

      <div className="main__body viz">
        <StatTiles stats={stats} />

        {showTable ? (
          <DataTables stats={stats} />
        ) : (
          <div className="chart-grid">
            <VolumeChart stats={stats} />
            <PriorityChart stats={stats} />
          </div>
        )}

        <div style={{ marginTop: '1.5rem' }}>
          <AgentLoadTable stats={stats} />
        </div>
      </div>
    </>
  );
}

/**
 * Headline figures.
 *
 * A stat tile is the right form when the answer is one number and its trend is
 * not the question. Plotting "43 open tickets" as a bar of length one tells the
 * reader nothing they could not read faster from the numeral.
 */
function StatTiles({ stats }: { stats: Stats }): JSX.Element {
  const attainmentTone =
    stats.resolution_attainment_pct >= 95
      ? 'stat__value--success'
      : stats.resolution_attainment_pct < 85
        ? 'stat__value--danger'
        : '';

  return (
    <div className="stat-grid">
      <div className="stat">
        <div className="stat__label">Open</div>
        <div className="stat__value">{stats.open}</div>
        <div className="stat__hint">{stats.created} raised in this window</div>
      </div>

      <div className="stat">
        <div className="stat__label">Breaching SLA</div>
        <div className={`stat__value${stats.breached > 0 ? ' stat__value--danger' : ''}`}>
          {stats.breached}
        </div>
        <div className="stat__hint">past their resolution target</div>
      </div>

      <div className="stat">
        <div className="stat__label">Resolution attainment</div>
        <div className={`stat__value ${attainmentTone}`}>
          {percent(stats.resolution_attainment_pct)}
        </div>
        <div className="stat__hint">
          first response {percent(stats.first_response_attainment_pct)}
        </div>
      </div>

      <div className="stat">
        <div className="stat__label">Resolution time</div>
        <div className="stat__value">{duration(stats.median_resolution_seconds)}</div>
        {/*
          The median is the headline and p90 sits beneath it. Support durations
          have a long tail, so a mean is dominated by the handful of tickets
          that sat for a month — p50 is the typical experience, p90 is the bad
          one, and reporting only a mean describes neither.
        */}
        <div className="stat__hint">p90 {duration(stats.p90_resolution_seconds)}</div>
      </div>

      <div className="stat">
        <div className="stat__label">Reopened</div>
        <div className={`stat__value${stats.reopened > 0 ? ' stat__value--danger' : ''}`}>
          {stats.reopened}
        </div>
        <div className="stat__hint">closed too early</div>
      </div>
    </div>
  );
}

const VOLUME_SERIES = [
  { key: 'created', label: 'Raised', color: 'var(--series-created)' },
  { key: 'resolved', label: 'Resolved', color: 'var(--series-resolved)' },
] as const;

function VolumeChart({ stats }: { stats: Stats }): JSX.Element {
  if (stats.volume.length === 0) {
    return (
      <Card title="Volume">
        <EmptyState title="No data in this window" />
      </Card>
    );
  }

  const data = stats.volume.map((point) => ({
    ...point,
    label: new Date(point.day).toLocaleDateString(undefined, { month: 'short', day: 'numeric' }),
  }));

  return (
    <Card title="Raised vs resolved">
      {/* A legend is always present for two or more series, so identity is
          never carried by colour alone. */}
      <div className="viz-legend">
        {VOLUME_SERIES.map((series) => (
          <span key={series.key} className="viz-legend__item">
            <span className="viz-legend__swatch" style={{ background: series.color }} />
            {series.label}
          </span>
        ))}
      </div>

      <ResponsiveContainer width="100%" height={240}>
        <LineChart data={data} margin={{ top: 8, right: 12, bottom: 0, left: -18 }}>
          {/* Recessive grid: horizontal only, so it aids reading values without
              competing with the data. */}
          <CartesianGrid stroke="var(--viz-grid)" vertical={false} />
          <XAxis
            dataKey="label"
            stroke="var(--viz-axis)"
            tick={{ fontSize: 11, fill: 'var(--viz-axis)' }}
            tickLine={false}
            axisLine={false}
            minTickGap={24}
          />
          <YAxis
            stroke="var(--viz-axis)"
            tick={{ fontSize: 11, fill: 'var(--viz-axis)' }}
            tickLine={false}
            axisLine={false}
            allowDecimals={false}
            width={44}
          />
          <Tooltip
            content={<VolumeTooltip />}
            cursor={{ stroke: 'var(--viz-axis)', strokeWidth: 1, strokeDasharray: '3 3' }}
          />
          {VOLUME_SERIES.map((series) => (
            <Line
              key={series.key}
              type="monotone"
              dataKey={series.key}
              name={series.label}
              stroke={series.color}
              strokeWidth={2}
              // No dot per point: at 90 days that is 180 marks competing with
              // the line they are meant to describe. The active dot appears on
              // hover, sized above the 8px minimum hit target.
              dot={false}
              activeDot={{ r: 4, strokeWidth: 2, stroke: 'var(--viz-surface)' }}
            />
          ))}
        </LineChart>
      </ResponsiveContainer>
    </Card>
  );
}

function VolumeTooltip({ active, payload, label }: TooltipProps<number, string>): JSX.Element | null {
  if (!active || !payload || payload.length === 0) return null;

  return (
    <div className="viz-tooltip">
      <div className="viz-tooltip__title">{label}</div>
      {payload.map((entry) => (
        <div key={entry.dataKey} className="viz-tooltip__row">
          <span className="viz-tooltip__swatch" style={{ background: entry.color }} />
          {entry.name}
          <span className="viz-tooltip__value">{entry.value}</span>
        </div>
      ))}
    </div>
  );
}

const PRIORITY_RAMP: Array<{ key: string; label: string; color: string }> = [
  { key: 'P1', label: 'P1 — critical', color: 'var(--ordinal-p1)' },
  { key: 'P2', label: 'P2 — high', color: 'var(--ordinal-p2)' },
  { key: 'P3', label: 'P3 — normal', color: 'var(--ordinal-p3)' },
  { key: 'P4', label: 'P4 — low', color: 'var(--ordinal-p4)' },
];

/**
 * Priority mix as a horizontal bar chart, built from plain HTML rather than
 * an SVG chart component.
 *
 * Four labelled bars do not need a charting library, and hand-built markup
 * gives direct value labels, real text for a screen reader, and no layout
 * surprises. The ramp is single-hue and ordered: darker reads as more severe.
 */
function PriorityChart({ stats }: { stats: Stats }): JSX.Element {
  const counts = PRIORITY_RAMP.map((entry) => ({
    ...entry,
    value: stats.by_priority[entry.key] ?? 0,
  }));
  const max = Math.max(...counts.map((entry) => entry.value), 1);
  const total = counts.reduce((sum, entry) => sum + entry.value, 0);

  return (
    <Card title="Priority mix">
      {total === 0 ? (
        <EmptyState title="No tickets in this window" />
      ) : (
        <div>
          {counts.map((entry) => (
            <div key={entry.key} className="viz-bar-row">
              <span className="mono subtle">{entry.key}</span>
              <span
                className="viz-bar-track"
                role="img"
                aria-label={`${entry.label}: ${entry.value} tickets`}
              >
                <span
                  className="viz-bar-fill"
                  style={{ width: `${(entry.value / max) * 100}%`, background: entry.color }}
                />
              </span>
              {/* Direct labels: four bars is few enough that every value can be
                  shown, which removes the need for an axis entirely. */}
              <span className="viz-bar-value">{entry.value}</span>
            </div>
          ))}
          <div className="subtle" style={{ marginTop: 12 }}>
            {total} ticket{total === 1 ? '' : 's'} raised in this window
          </div>
        </div>
      )}
    </Card>
  );
}

/** The table view, so the numbers are readable without relying on colour. */
function DataTables({ stats }: { stats: Stats }): JSX.Element {
  return (
    <div className="chart-grid">
      <Card title="Volume by day">
        <table className="table">
          <thead>
            <tr>
              <th>Day</th>
              <th className="table__number">Raised</th>
              <th className="table__number">Resolved</th>
            </tr>
          </thead>
          <tbody>
            {stats.volume.map((point) => (
              <tr key={point.day}>
                <td>{point.day}</td>
                <td className="table__number">{point.created}</td>
                <td className="table__number">{point.resolved}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>

      <Card title="Breakdown">
        <table className="table">
          <thead>
            <tr>
              <th>Status</th>
              <th className="table__number">Tickets</th>
            </tr>
          </thead>
          <tbody>
            {Object.entries(stats.by_status).map(([status, count]) => (
              <tr key={status}>
                <td>{status.replace(/_/g, ' ')}</td>
                <td className="table__number">{count}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
    </div>
  );
}

/**
 * Where the queue is actually stuck.
 *
 * A table rather than a chart: the reader wants to find a specific person's
 * row and read an exact number, which is what tables are for. A bar chart of
 * agent names would make both of those harder.
 */
function AgentLoadTable({ stats }: { stats: Stats }): JSX.Element {
  if (stats.agent_load.length === 0) {
    return (
      <Card title="Workload">
        <EmptyState title="Nothing assigned" description="No agent currently holds an open ticket." />
      </Card>
    );
  }

  return (
    <Card title="Workload by agent">
      <table className="table">
        <thead>
          <tr>
            <th>Agent</th>
            <th className="table__number">Open</th>
            <th className="table__number">Breaching</th>
          </tr>
        </thead>
        <tbody>
          {stats.agent_load.map((agent) => (
            <tr key={agent.user_id}>
              <td>{agent.full_name}</td>
              <td className="table__number">{agent.open}</td>
              <td className="table__number">
                {agent.breached > 0 ? (
                  <span className="badge badge--danger">{agent.breached}</span>
                ) : (
                  <span className="subtle">0</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Card>
  );
}
