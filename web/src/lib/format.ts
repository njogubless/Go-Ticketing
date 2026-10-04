import type { Priority, TicketKind, TicketStatus } from '@/api/types';

/**
 * Presentation helpers.
 *
 * These are the only place raw enum values become human-readable text, so a
 * status added on the backend produces one compile error here rather than
 * three raw `pending_requester` strings leaking into the UI.
 */

const STATUS_LABELS: Record<TicketStatus, string> = {
  new: 'New',
  triaged: 'Triaged',
  pending_approval: 'Awaiting approval',
  in_progress: 'In progress',
  pending_requester: 'Waiting on requester',
  resolved: 'Resolved',
  closed: 'Closed',
  cancelled: 'Cancelled',
};

export function statusLabel(status: TicketStatus): string {
  return STATUS_LABELS[status];
}

/** Maps a status onto a badge variant. */
export function statusTone(status: TicketStatus): string {
  switch (status) {
    case 'new':
      return 'badge--info';
    case 'in_progress':
      return 'badge--info';
    case 'pending_requester':
    case 'pending_approval':
      return 'badge--warning';
    case 'resolved':
      return 'badge--success';
    case 'closed':
    case 'cancelled':
      return 'badge--neutral';
    case 'triaged':
      return 'badge--neutral';
  }
}

const KIND_LABELS: Record<TicketKind, string> = {
  incident: 'Incident',
  service_request: 'Service request',
  change: 'Change',
  problem: 'Problem',
};

export function kindLabel(kind: TicketKind): string {
  return KIND_LABELS[kind];
}

export function priorityClass(priority: Priority): string {
  return `badge--${priority.toLowerCase()}`;
}

/**
 * A relative timestamp: "in 3h", "2d ago".
 *
 * Relative rather than absolute because the question an agent is actually
 * asking is "how long have I got?", and answering it with "14:32" makes them
 * do the arithmetic. The absolute time goes in the title attribute for when
 * the exact value matters.
 */
export function relativeTime(iso: string | undefined): string {
  if (!iso) return '—';
  const target = new Date(iso).getTime();
  if (Number.isNaN(target)) return '—';

  const deltaSeconds = Math.round((target - Date.now()) / 1000);
  const absolute = Math.abs(deltaSeconds);

  const units: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ['second', 60],
    ['minute', 60],
    ['hour', 24],
    ['day', 7],
    ['week', 4.35],
    ['month', 12],
    ['year', Number.POSITIVE_INFINITY],
  ];

  let value = deltaSeconds;
  for (const entry of units) {
    const unit = entry[0];
    const step = entry[1];
    if (Math.abs(value) < step) {
      return new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' }).format(
        Math.round(value),
        unit,
      );
    }
    value /= step;
  }
  // Reaching here means the value exceeded every unit; `absolute` keeps the
  // fallback honest rather than returning a misleading "just now".
  return absolute > 0 ? 'a long time ago' : 'now';
}

export function absoluteTime(iso: string | undefined): string {
  if (!iso) return '';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  return date.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

/** Formats a duration in seconds as "4h 20m" — the shape a report reads best. */
export function duration(seconds: number): string {
  if (!seconds || seconds <= 0) return '—';
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.round((seconds % 3600) / 60);
  if (hours === 0) return `${minutes}m`;
  if (hours < 48) return minutes > 0 ? `${hours}h ${minutes}m` : `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}

export function percent(value: number): string {
  return `${value.toFixed(1)}%`;
}

/**
 * Classifies an SLA deadline so the queue can highlight it.
 *
 * "Soon" is two hours: long enough that an agent can still act, short enough
 * that flagging it means something. Flagging everything due today would mark
 * most of the queue and therefore mark nothing.
 */
export function dueTone(iso: string | undefined, breached: boolean): string {
  if (breached) return 'due--breached';
  if (!iso) return '';
  const remaining = new Date(iso).getTime() - Date.now();
  if (remaining < 2 * 60 * 60 * 1000) return 'due--soon';
  return '';
}

export function initials(fullName: string): string {
  return fullName
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase() ?? '')
    .join('');
}
