import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { Impact, TicketDetail, TicketStatus, Urgency, Visibility } from '@/api/types';
import { Permission, useAuth } from '@/auth/AuthContext';
import {
  Card,
  EmptyState,
  ErrorMessage,
  Field,
  PriorityBadge,
  Spinner,
  StatusBadge,
} from '@/components/primitives';
import { absoluteTime, kindLabel, relativeTime, statusLabel } from '@/lib/format';

/**
 * Ticket detail — the screen where work actually happens.
 *
 * Two things here are worth pointing at:
 *
 *  1. The action buttons are rendered from `allowed_transitions`, which the
 *     server computes from the same state machine it enforces. The client does
 *     not know the transition rules and must not: a second copy of them would
 *     drift, and the drift would show as buttons that fail.
 *
 *  2. Writes carry the ETag from the last read as an optimistic lock. Two
 *     agents resolving the same ticket from stale tabs is routine on a busy
 *     desk, and without this the second write silently overwrites the first.
 */
export function TicketPage(): JSX.Element {
  const { id } = useParams<{ id: string }>();
  const { can } = useAuth();
  const queryClient = useQueryClient();
  const [actionError, setActionError] = useState<unknown>(null);

  const ticketQuery = useQuery({
    queryKey: ['ticket', id],
    queryFn: () => api.getTicket(id ?? ''),
    enabled: Boolean(id),
  });

  const ticket = ticketQuery.data?.data;
  const etag = ticketQuery.data?.etag ?? null;

  const invalidate = (): void => {
    void queryClient.invalidateQueries({ queryKey: ['ticket', id] });
    void queryClient.invalidateQueries({ queryKey: ['tickets'] });
  };

  const transition = useMutation({
    mutationFn: (input: { status: TicketStatus; resolution?: string }) =>
      api.transitionTicket(id ?? '', input.status, input.resolution, etag),
    onSuccess: () => {
      setActionError(null);
      invalidate();
    },
    onError: setActionError,
  });

  const assign = useMutation({
    mutationFn: (assigneeId: string | null) => api.assignTicket(id ?? '', assigneeId, etag),
    onSuccess: () => {
      setActionError(null);
      invalidate();
    },
    onError: setActionError,
  });

  const reclassify = useMutation({
    mutationFn: (input: { impact: Impact; urgency: Urgency }) =>
      api.reclassifyTicket(id ?? '', input.impact, input.urgency, etag),
    onSuccess: () => {
      setActionError(null);
      invalidate();
    },
    onError: setActionError,
  });

  const addMessage = useMutation({
    mutationFn: (input: { body: string; visibility: Visibility }) =>
      api.addMessage(id ?? '', input.body, input.visibility),
    onSuccess: () => {
      setActionError(null);
      invalidate();
    },
    onError: setActionError,
  });

  if (ticketQuery.isLoading) {
    return (
      <div className="main__body">
        <Spinner label="Loading ticket" />
      </div>
    );
  }

  if (ticketQuery.isError || !ticket) {
    return (
      <div className="main__body">
        <ErrorMessage error={ticketQuery.error} />
        <Link to="/" className="button">
          Back
        </Link>
      </div>
    );
  }

  return (
    <>
      <header className="main__header">
        <div style={{ minWidth: 0 }}>
          <div className="row row--wrap" style={{ marginBottom: 2 }}>
            <span className="mono subtle">{ticket.reference}</span>
            <PriorityBadge priority={ticket.priority} />
            <StatusBadge status={ticket.status} />
            <span className="badge badge--neutral">{kindLabel(ticket.kind)}</span>
            {ticket.breached ? <span className="badge badge--danger">SLA breached</span> : null}
            {ticket.paused_since ? (
              <span className="badge badge--warning" title="The SLA clock is stopped">
                clock paused
              </span>
            ) : null}
          </div>
          <h1>{ticket.subject}</h1>
        </div>
      </header>

      <div className="main__body">
        <ErrorMessage error={actionError} />

        <div className="detail">
          <div className="stack">
            <Card title="Description">
              <div className="message__body">{ticket.description}</div>
            </Card>

            {ticket.resolution ? (
              <Card title="Resolution">
                <div className="message__body">{ticket.resolution}</div>
                <div className="subtle" style={{ marginTop: 8 }}>
                  Resolved {relativeTime(ticket.resolved_at)}
                </div>
              </Card>
            ) : null}

            <Thread ticket={ticket} />

            {ticket.status !== 'closed' && ticket.status !== 'cancelled' ? (
              <Composer
                canPostInternal={can(Permission.NoteInternal)}
                pending={addMessage.isPending}
                onSubmit={(body, visibility) => addMessage.mutate({ body, visibility })}
              />
            ) : null}

            {can(Permission.AuditRead) ? <AuditTrail ticketId={ticket.id} /> : null}
          </div>

          <div className="stack">
            {can(Permission.TicketTransition) && ticket.allowed_transitions.length > 0 ? (
              <Card title="Actions">
                <TransitionButtons
                  ticket={ticket}
                  pending={transition.isPending}
                  onTransition={(status, resolution) =>
                    transition.mutate(resolution ? { status, resolution } : { status })
                  }
                />
              </Card>
            ) : null}

            <Card title="Details">
              <div className="meta-list">
                <MetaRow label="Requester" value={ticket.requester?.full_name ?? '—'} />
                <MetaRow label="Assignee" value={ticket.assignee?.full_name ?? 'Unassigned'} />
                <MetaRow label="Impact" value={ticket.impact} />
                <MetaRow label="Urgency" value={ticket.urgency} />
                <MetaRow label="Category" value={ticket.category || '—'} />
                <MetaRow
                  label="Created"
                  value={relativeTime(ticket.created_at)}
                  title={absoluteTime(ticket.created_at)}
                />
                <MetaRow
                  label="First response"
                  value={
                    ticket.first_response_at
                      ? `${relativeTime(ticket.first_response_at)}`
                      : `due ${relativeTime(ticket.first_response_due)}`
                  }
                  title={absoluteTime(ticket.first_response_due)}
                />
                <MetaRow
                  label="Resolution due"
                  value={relativeTime(ticket.resolution_due)}
                  title={absoluteTime(ticket.resolution_due)}
                />
                {ticket.reopen_count > 0 ? (
                  <MetaRow label="Reopened" value={`${ticket.reopen_count}×`} />
                ) : null}
              </div>

              {ticket.tags.length > 0 ? (
                <div className="row row--wrap" style={{ marginTop: 12 }}>
                  {ticket.tags.map((tag) => (
                    <span key={tag} className="tag">
                      {tag}
                    </span>
                  ))}
                </div>
              ) : null}
            </Card>

            {can(Permission.TicketAssign) ? (
              <AssignPanel
                ticket={ticket}
                pending={assign.isPending}
                onAssign={(userId) => assign.mutate(userId)}
              />
            ) : null}

            {can(Permission.TicketTransition) && !isTerminal(ticket.status) ? (
              <ReclassifyPanel
                ticket={ticket}
                pending={reclassify.isPending}
                onReclassify={(impact, urgency) => reclassify.mutate({ impact, urgency })}
              />
            ) : null}

            {ticket.assets.length > 0 ? (
              <Card title="Affected assets">
                <div className="stack" style={{ gap: 8 }}>
                  {ticket.assets.map((asset) => (
                    <div key={asset.id} className="row row--between">
                      <span>
                        <span className="mono subtle">{asset.tag}</span> {asset.name}
                      </span>
                      <span className="badge badge--neutral">{asset.criticality}</span>
                    </div>
                  ))}
                </div>
              </Card>
            ) : null}

            {ticket.approvals.length > 0 ? (
              <Card title="Approvals">
                <div className="stack" style={{ gap: 8 }}>
                  {ticket.approvals.map((approval) => (
                    <div key={approval.id} className="row row--between">
                      <span className="subtle mono">{approval.approver_id.slice(0, 8)}</span>
                      <span
                        className={`badge ${
                          approval.decision === 'approved'
                            ? 'badge--success'
                            : approval.decision === 'rejected'
                              ? 'badge--danger'
                              : 'badge--warning'
                        }`}
                      >
                        {approval.decision}
                      </span>
                    </div>
                  ))}
                </div>
              </Card>
            ) : null}
          </div>
        </div>
      </div>
    </>
  );
}

function isTerminal(status: TicketStatus): boolean {
  return status === 'closed' || status === 'cancelled';
}

function MetaRow({
  label,
  value,
  title,
}: {
  label: string;
  value: string;
  title?: string;
}): JSX.Element {
  return (
    <div className="meta-row">
      <span className="meta-row__label">{label}</span>
      <span title={title}>{value}</span>
    </div>
  );
}

/**
 * Renders one button per server-permitted transition.
 *
 * Resolving is the exception: it needs a note, so it opens a small form rather
 * than firing immediately. The server enforces that requirement too — this is
 * only about not making the agent discover it via an error.
 */
function TransitionButtons({
  ticket,
  pending,
  onTransition,
}: {
  ticket: TicketDetail;
  pending: boolean;
  onTransition: (status: TicketStatus, resolution?: string) => void;
}): JSX.Element {
  const [resolving, setResolving] = useState(false);
  const [resolution, setResolution] = useState('');

  if (resolving) {
    return (
      <form
        onSubmit={(event) => {
          event.preventDefault();
          onTransition('resolved', resolution);
          setResolving(false);
          setResolution('');
        }}
      >
        <Field
          label="What fixed it?"
          htmlFor="resolution"
          hint="This is sent to the requester and kept as the ticket's record."
        >
          <textarea
            id="resolution"
            className="textarea"
            value={resolution}
            onChange={(event) => setResolution(event.target.value)}
            required
            autoFocus
          />
        </Field>
        <div className="row">
          <button type="submit" className="button button--primary" disabled={pending}>
            Resolve
          </button>
          <button type="button" className="button button--ghost" onClick={() => setResolving(false)}>
            Cancel
          </button>
        </div>
      </form>
    );
  }

  return (
    <div className="transitions">
      {ticket.allowed_transitions.map((status) => (
        <button
          key={status}
          type="button"
          className={`button${status === 'resolved' ? ' button--primary' : ''}${
            status === 'cancelled' ? ' button--danger' : ''
          }`}
          disabled={pending}
          onClick={() => {
            if (status === 'resolved') setResolving(true);
            else onTransition(status);
          }}
        >
          {actionLabel(status)}
        </button>
      ))}
    </div>
  );
}

/** Buttons read as verbs; the status badge already states the noun. */
function actionLabel(status: TicketStatus): string {
  switch (status) {
    case 'triaged':
      return 'Send to queue';
    case 'in_progress':
      return 'Start work';
    case 'pending_requester':
      return 'Wait on requester';
    case 'pending_approval':
      return 'Request approval';
    case 'resolved':
      return 'Resolve';
    case 'closed':
      return 'Close';
    case 'cancelled':
      return 'Cancel';
    case 'new':
      return 'Reopen';
  }
}

function Thread({ ticket }: { ticket: TicketDetail }): JSX.Element {
  if (ticket.messages.length === 0) {
    return (
      <Card title="Conversation">
        <EmptyState title="No messages yet" description="Replies will appear here." />
      </Card>
    );
  }

  return (
    <Card
      title="Conversation"
      action={
        !ticket.can_read_internal ? (
          // Say so explicitly. A requester seeing a partial thread with no
          // indication would reasonably assume it is the whole thread.
          <span className="subtle">Internal notes are hidden</span>
        ) : null
      }
    >
      <div className="thread">
        {ticket.messages.map((message) => (
          <article
            key={message.id}
            className={`message${message.visibility === 'internal' ? ' message--internal' : ''}${
              message.system ? ' message--system' : ''
            }`}
          >
            <header className="message__header">
              <span className="message__author">
                {message.system
                  ? 'System'
                  : message.author_id === ticket.requester?.id
                    ? (ticket.requester?.full_name ?? 'Requester')
                    : message.author_id === ticket.assignee?.id
                      ? (ticket.assignee?.full_name ?? 'Agent')
                      : 'Service desk'}
              </span>
              <span className="row" style={{ gap: 8 }}>
                {message.visibility === 'internal' ? (
                  <span className="badge badge--warning">internal</span>
                ) : null}
                <time dateTime={message.created_at} title={absoluteTime(message.created_at)}>
                  {relativeTime(message.created_at)}
                </time>
              </span>
            </header>
            <div className="message__body">{message.body}</div>
          </article>
        ))}
      </div>
    </Card>
  );
}

function Composer({
  canPostInternal,
  pending,
  onSubmit,
}: {
  canPostInternal: boolean;
  pending: boolean;
  onSubmit: (body: string, visibility: Visibility) => void;
}): JSX.Element {
  const [body, setBody] = useState('');
  const [internal, setInternal] = useState(false);

  const submit = (): void => {
    if (!body.trim()) return;
    onSubmit(body.trim(), internal ? 'internal' : 'public');
    setBody('');
  };

  return (
    <Card>
      <form
        className="composer"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <textarea
          className="textarea"
          value={body}
          placeholder={internal ? 'Internal note — the requester will not see this' : 'Reply to the requester…'}
          onChange={(event) => setBody(event.target.value)}
          onKeyDown={(event) => {
            // Ctrl/Cmd+Enter sends. Plain Enter must insert a newline — an
            // agent typing a multi-line reply should not have it sent halfway.
            if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
              event.preventDefault();
              submit();
            }
          }}
          // The whole composer changes colour with the visibility toggle, so
          // there is no way to type an internal note thinking it is public.
          style={
            internal
              ? { borderColor: 'var(--warning)', background: 'var(--warning-soft)' }
              : undefined
          }
        />
        <div className="composer__actions">
          {canPostInternal ? (
            <label className="composer__toggle">
              <input
                type="checkbox"
                checked={internal}
                onChange={(event) => setInternal(event.target.checked)}
              />
              Internal note
            </label>
          ) : (
            <span />
          )}
          <button type="submit" className="button button--primary" disabled={pending || !body.trim()}>
            {pending ? 'Sending…' : internal ? 'Add note' : 'Send reply'}
          </button>
        </div>
      </form>
    </Card>
  );
}

function AssignPanel({
  ticket,
  pending,
  onAssign,
}: {
  ticket: TicketDetail;
  pending: boolean;
  onAssign: (userId: string | null) => void;
}): JSX.Element {
  const usersQuery = useQuery({
    queryKey: ['users', 'assignable'],
    queryFn: () => api.listUsers({ role: 'agent' }),
  });

  return (
    <Card title="Assignment">
      <select
        className="select"
        value={ticket.assignee_id ?? ''}
        disabled={pending || usersQuery.isLoading}
        onChange={(event) => onAssign(event.target.value === '' ? null : event.target.value)}
        aria-label="Assignee"
      >
        <option value="">Unassigned</option>
        {usersQuery.data?.items.map((user) => (
          <option key={user.id} value={user.id}>
            {user.full_name}
          </option>
        ))}
      </select>
    </Card>
  );
}

function ReclassifyPanel({
  ticket,
  pending,
  onReclassify,
}: {
  ticket: TicketDetail;
  pending: boolean;
  onReclassify: (impact: Impact, urgency: Urgency) => void;
}): JSX.Element {
  const [impact, setImpact] = useState<Impact>(ticket.impact);
  const [urgency, setUrgency] = useState<Urgency>(ticket.urgency);
  const changed = impact !== ticket.impact || urgency !== ticket.urgency;

  return (
    <Card title="Classification">
      <Field label="Impact" htmlFor="impact" hint="How much of the organisation is affected?">
        <select
          id="impact"
          className="select"
          value={impact}
          onChange={(event) => setImpact(event.target.value as Impact)}
        >
          <option value="low">Low — one person</option>
          <option value="medium">Medium — a team</option>
          <option value="high">High — organisation-wide</option>
        </select>
      </Field>

      <Field label="Urgency" htmlFor="urgency" hint="How fast is it getting worse?">
        <select
          id="urgency"
          className="select"
          value={urgency}
          onChange={(event) => setUrgency(event.target.value as Urgency)}
        >
          <option value="low">Low — a workaround exists</option>
          <option value="medium">Medium — degraded</option>
          <option value="high">High — work is stopped</option>
        </select>
      </Field>

      {/* Priority is derived, so show the consequence before they commit. */}
      <div className="subtle" style={{ marginBottom: 12 }}>
        Priority will be <strong>{derivePriority(impact, urgency)}</strong>
      </div>

      <button
        type="button"
        className="button button--primary"
        disabled={!changed || pending}
        onClick={() => onReclassify(impact, urgency)}
      >
        Update classification
      </button>
    </Card>
  );
}

/**
 * A mirror of the server's priority matrix, used only to preview the outcome
 * before the agent commits. The server remains authoritative — this never
 * decides anything, it only avoids making someone save to find out.
 */
function derivePriority(impact: Impact, urgency: Urgency): string {
  const matrix: Record<Impact, Record<Urgency, string>> = {
    high: { high: 'P1', medium: 'P2', low: 'P3' },
    medium: { high: 'P2', medium: 'P3', low: 'P4' },
    low: { high: 'P3', medium: 'P4', low: 'P4' },
  };
  return matrix[impact][urgency];
}

function AuditTrail({ ticketId }: { ticketId: string }): JSX.Element | null {
  const [open, setOpen] = useState(false);
  const auditQuery = useQuery({
    queryKey: ['audit', ticketId],
    queryFn: () => api.auditTrail(ticketId),
    enabled: open,
  });

  return (
    <Card
      title="History"
      action={
        <button type="button" className="button button--ghost button--sm" onClick={() => setOpen(!open)}>
          {open ? 'Hide' : 'Show'}
        </button>
      }
    >
      {!open ? (
        <div className="subtle">Every change to this ticket, with who made it and when.</div>
      ) : auditQuery.isLoading ? (
        <Spinner label="Loading history" />
      ) : (
        <table className="table">
          <thead>
            <tr>
              <th>When</th>
              <th>Who</th>
              <th>What</th>
            </tr>
          </thead>
          <tbody>
            {auditQuery.data?.items.map((entry) => (
              <tr key={entry.id}>
                <td className="nowrap" title={absoluteTime(entry.created_at)}>
                  {relativeTime(entry.created_at)}
                </td>
                <td>{entry.actor_label}</td>
                <td>
                  {entry.action.replace('ticket.', '').replace(/_/g, ' ')}
                  {entry.from && entry.to ? (
                    <span className="subtle"> · {entry.from} → {entry.to}</span>
                  ) : entry.to ? (
                    <span className="subtle"> · {statusLabelSafe(entry.to)}</span>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}

/** Audit values are free-form strings; only translate the ones we recognise. */
function statusLabelSafe(value: string): string {
  const known: TicketStatus[] = [
    'new', 'triaged', 'pending_approval', 'in_progress',
    'pending_requester', 'resolved', 'closed', 'cancelled',
  ];
  return known.includes(value as TicketStatus) ? statusLabel(value as TicketStatus) : value;
}
