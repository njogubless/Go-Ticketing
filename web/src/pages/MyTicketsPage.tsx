import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { useAuth, useIsStaff } from '@/auth/AuthContext';
import {
  Card,
  EmptyState,
  ErrorMessage,
  PriorityBadge,
  Spinner,
  StatusBadge,
} from '@/components/primitives';
import { absoluteTime, kindLabel, relativeTime } from '@/lib/format';

/**
 * The requester's view of their own tickets.
 *
 * Deliberately calmer than the agent queue. A requester wants to know "where
 * is my thing?", not to triage a workload — so this leads with status in plain
 * words and omits the SLA and priority machinery that only means something to
 * someone working the desk.
 *
 * The server scopes this to the caller regardless of what is requested; the
 * requester_id filter here only narrows an agent's own view of their tickets.
 */
export function MyTicketsPage(): JSX.Element {
  const { user } = useAuth();
  const isStaff = useIsStaff();

  const ticketsQuery = useQuery({
    queryKey: ['tickets', 'mine', user?.id],
    queryFn: () =>
      api.listTickets({
        ...(user ? { requester_id: user.id } : {}),
        sort: 'updated',
        limit: 50,
      }),
    enabled: Boolean(user),
  });

  const tickets = ticketsQuery.data?.items ?? [];
  const open = tickets.filter(
    (ticket) => ticket.status !== 'closed' && ticket.status !== 'cancelled',
  );
  const closed = tickets.filter(
    (ticket) => ticket.status === 'closed' || ticket.status === 'cancelled',
  );

  return (
    <>
      <header className="main__header">
        <div>
          <h1>My tickets</h1>
          <div className="subtle">Everything you have raised with the service desk</div>
        </div>
        <Link to="/tickets/new" className="button button--primary">
          Raise a ticket
        </Link>
      </header>

      <div className="main__body">
        {ticketsQuery.isLoading ? (
          <Spinner label="Loading your tickets" />
        ) : ticketsQuery.isError ? (
          <ErrorMessage error={ticketsQuery.error} />
        ) : tickets.length === 0 ? (
          <Card>
            <EmptyState
              title="No tickets yet"
              description="When you raise something with the service desk it will appear here."
              action={
                <Link to="/tickets/new" className="button button--primary">
                  Raise your first ticket
                </Link>
              }
            />
          </Card>
        ) : (
          <div className="stack">
            <Card title={`Open (${open.length})`}>
              {open.length === 0 ? (
                <div className="subtle">Nothing outstanding.</div>
              ) : (
                <div className="stack" style={{ gap: 0 }}>
                  {open.map((ticket) => (
                    <Link
                      key={ticket.id}
                      to={`/tickets/${ticket.id}`}
                      className="ticket-row"
                      style={{ padding: '0.75rem 0' }}
                    >
                      <span className="ticket-row__reference">{ticket.reference}</span>
                      <span className="ticket-row__main">
                        <span className="ticket-row__subject">{ticket.subject}</span>
                        <span className="ticket-row__meta">
                          <span>{kindLabel(ticket.kind)}</span>
                          <StatusBadge status={ticket.status} />
                          <span title={absoluteTime(ticket.updated_at)}>
                            updated {relativeTime(ticket.updated_at)}
                          </span>
                        </span>
                      </span>
                      <span className="ticket-row__side">
                        {/* Staff see the priority; a requester does not need it
                            and showing it invites arguments about it. */}
                        {isStaff ? <PriorityBadge priority={ticket.priority} /> : null}
                      </span>
                    </Link>
                  ))}
                </div>
              )}
            </Card>

            {closed.length > 0 ? (
              <Card title={`Closed (${closed.length})`}>
                <div className="stack" style={{ gap: 0 }}>
                  {closed.map((ticket) => (
                    <Link
                      key={ticket.id}
                      to={`/tickets/${ticket.id}`}
                      className="ticket-row"
                      style={{ padding: '0.75rem 0' }}
                    >
                      <span className="ticket-row__reference">{ticket.reference}</span>
                      <span className="ticket-row__main">
                        <span className="ticket-row__subject">{ticket.subject}</span>
                        <span className="ticket-row__meta">
                          <StatusBadge status={ticket.status} />
                          <span title={absoluteTime(ticket.closed_at ?? ticket.updated_at)}>
                            {relativeTime(ticket.closed_at ?? ticket.updated_at)}
                          </span>
                        </span>
                      </span>
                      <span />
                    </Link>
                  ))}
                </div>
              </Card>
            ) : null}
          </div>
        )}
      </div>
    </>
  );
}
