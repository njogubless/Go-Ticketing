import { useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { Priority, TicketFilters, TicketStatus, TicketSummary } from '@/api/types';
import { useAuth } from '@/auth/AuthContext';
import { EmptyState, ErrorMessage, PriorityBadge, Spinner, StatusBadge } from '@/components/primitives';
import { absoluteTime, dueTone, kindLabel, relativeTime } from '@/lib/format';

/**
 * The agent queue — the screen a service desk lives in.
 *
 * Filters are held in the URL rather than in component state. That is what
 * makes a queue shareable: an agent can paste "the P1s breaching today" into
 * chat and a colleague sees the same list. It also survives a refresh, which
 * component state does not.
 */

const OPEN_STATUSES: TicketStatus[] = [
  'new',
  'triaged',
  'pending_approval',
  'in_progress',
  'pending_requester',
];

interface Preset {
  id: string;
  label: string;
  filters: TicketFilters;
}

export function QueuePage(): JSX.Element {
  const { user } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();
  const [searchDraft, setSearchDraft] = useState(searchParams.get('q') ?? '');

  const presets = useMemo<Preset[]>(
    () => [
      { id: 'open', label: 'All open', filters: { status: OPEN_STATUSES, sort: 'priority' } },
      {
        id: 'mine',
        label: 'Assigned to me',
        filters: {
          status: OPEN_STATUSES,
          ...(user ? { assignee_id: user.id } : {}),
          sort: 'due_soonest',
        },
      },
      {
        id: 'unassigned',
        label: 'Unassigned',
        filters: { status: ['new', 'triaged'], unassigned: true, sort: 'priority' },
      },
      { id: 'breaching', label: 'Breaching SLA', filters: { breached: true, sort: 'due_soonest' } },
      {
        id: 'approvals',
        label: 'Awaiting approval',
        filters: { status: ['pending_approval'], kind: ['change'], sort: 'oldest' },
      },
    ],
    [user],
  );

  const activePresetId = searchParams.get('preset') ?? 'open';
  const activePreset = presets.find((preset) => preset.id === activePresetId) ?? presets[0];

  const filters = useMemo<TicketFilters>(() => {
    const base: TicketFilters = { ...(activePreset?.filters ?? {}) };
    const search = searchParams.get('q');
    if (search) base.q = search;
    const priority = searchParams.get('priority');
    if (priority) base.priority = priority.split(',') as Priority[];
    return base;
  }, [activePreset, searchParams]);

  const ticketsQuery = useQuery({
    queryKey: ['tickets', filters],
    queryFn: () => api.listTickets({ ...filters, limit: 50 }),
  });

  const applyPreset = (presetId: string): void => {
    const next = new URLSearchParams(searchParams);
    next.set('preset', presetId);
    setSearchParams(next, { replace: true });
  };

  const applySearch = (): void => {
    const next = new URLSearchParams(searchParams);
    if (searchDraft.trim()) next.set('q', searchDraft.trim());
    else next.delete('q');
    setSearchParams(next, { replace: true });
  };

  const togglePriority = (priority: Priority): void => {
    const current = new Set((searchParams.get('priority') ?? '').split(',').filter(Boolean));
    if (current.has(priority)) current.delete(priority);
    else current.add(priority);

    const next = new URLSearchParams(searchParams);
    if (current.size > 0) next.set('priority', [...current].join(','));
    else next.delete('priority');
    setSearchParams(next, { replace: true });
  };

  const selectedPriorities = new Set((searchParams.get('priority') ?? '').split(',').filter(Boolean));
  const tickets = ticketsQuery.data?.items ?? [];
  const breachingCount = tickets.filter((ticket) => ticket.breached).length;

  return (
    <>
      <header className="main__header">
        <div>
          <h1>Queue</h1>
          <div className="subtle">
            {ticketsQuery.isLoading
              ? 'Loading…'
              : `${tickets.length}${ticketsQuery.data?.has_more ? '+' : ''} ticket${tickets.length === 1 ? '' : 's'}`}
            {breachingCount > 0 ? ` · ${breachingCount} breaching` : ''}
          </div>
        </div>
        <Link to="/tickets/new" className="button button--primary">
          Raise a ticket
        </Link>
      </header>

      <div className="queue">
        <div className="queue__toolbar">
          {presets.map((preset) => (
            <button
              key={preset.id}
              type="button"
              className={`button button--sm${preset.id === activePresetId ? ' button--primary' : ''}`}
              onClick={() => applyPreset(preset.id)}
            >
              {preset.label}
            </button>
          ))}

          <span className="spacer" />

          {(['P1', 'P2', 'P3', 'P4'] as Priority[]).map((priority) => (
            <button
              key={priority}
              type="button"
              className={`button button--sm${selectedPriorities.has(priority) ? ' button--primary' : ''}`}
              onClick={() => togglePriority(priority)}
              aria-pressed={selectedPriorities.has(priority)}
            >
              {priority}
            </button>
          ))}

          <form
            className="queue__search"
            onSubmit={(event) => {
              event.preventDefault();
              applySearch();
            }}
          >
            <input
              className="input"
              type="search"
              placeholder="Search tickets…"
              value={searchDraft}
              onChange={(event) => setSearchDraft(event.target.value)}
              aria-label="Search tickets"
            />
          </form>
        </div>

        <div className="queue__scroll">
          {ticketsQuery.isLoading ? (
            <div style={{ padding: '2rem' }}>
              <Spinner label="Loading tickets" />
            </div>
          ) : ticketsQuery.isError ? (
            <div style={{ padding: '1.5rem' }}>
              <ErrorMessage error={ticketsQuery.error} />
            </div>
          ) : tickets.length === 0 ? (
            <EmptyState
              title="Nothing here"
              description="No tickets match this view. Try another filter."
            />
          ) : (
            tickets.map((ticket) => <TicketRow key={ticket.id} ticket={ticket} />)
          )}
        </div>
      </div>
    </>
  );
}

function TicketRow({ ticket }: { ticket: TicketSummary }): JSX.Element {
  return (
    <Link
      to={`/tickets/${ticket.id}`}
      className={`ticket-row${ticket.breached ? ' ticket-row--breached' : ''}`}
    >
      <span className="ticket-row__reference">{ticket.reference}</span>

      <span className="ticket-row__main">
        <span className="ticket-row__subject">{ticket.subject}</span>
        <span className="ticket-row__meta">
          <span>{kindLabel(ticket.kind)}</span>
          <StatusBadge status={ticket.status} />
          {ticket.reopen_count > 0 ? (
            <span className="badge badge--warning" title="This ticket has been reopened">
              reopened ×{ticket.reopen_count}
            </span>
          ) : null}
          {ticket.tags.slice(0, 3).map((tag) => (
            <span key={tag} className="tag">
              {tag}
            </span>
          ))}
        </span>
      </span>

      <span className="ticket-row__side">
        <span
          className={`due ${dueTone(ticket.resolution_due, ticket.breached)}`}
          title={absoluteTime(ticket.resolution_due)}
        >
          {ticket.breached ? 'Breached' : `due ${relativeTime(ticket.resolution_due)}`}
        </span>
        <PriorityBadge priority={ticket.priority} />
      </span>
    </Link>
  );
}
