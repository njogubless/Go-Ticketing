import type { ReactNode } from 'react';
import { ApiError } from '@/api/client';
import type { Priority, TicketStatus } from '@/api/types';
import { priorityClass, statusLabel, statusTone } from '@/lib/format';

/** Small shared presentational pieces. Deliberately dumb — no data fetching. */

export function Badge({
  children,
  tone = 'badge--neutral',
  title,
}: {
  children: ReactNode;
  tone?: string;
  title?: string;
}): JSX.Element {
  return (
    <span className={`badge ${tone}`} title={title}>
      {children}
    </span>
  );
}

export function PriorityBadge({ priority }: { priority: Priority }): JSX.Element {
  return (
    <Badge tone={priorityClass(priority)} title={`Priority ${priority}`}>
      {priority}
    </Badge>
  );
}

export function StatusBadge({ status }: { status: TicketStatus }): JSX.Element {
  return <Badge tone={statusTone(status)}>{statusLabel(status)}</Badge>;
}

export function Spinner({ label = 'Loading' }: { label?: string }): JSX.Element {
  return (
    <div className="row" role="status">
      <span className="spinner" aria-hidden="true" />
      <span className="visually-hidden">{label}</span>
    </div>
  );
}

export function EmptyState({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}): JSX.Element {
  return (
    <div className="empty">
      <div className="empty__title">{title}</div>
      {description ? <div className="subtle">{description}</div> : null}
      {action}
    </div>
  );
}

/**
 * Renders an error in terms the reader can act on.
 *
 * The two cases worth special-casing are the ones an agent will actually hit:
 * a stale write (someone edited the ticket first) and a state-machine refusal
 * (the action is not available from this status). Both have a next step, and
 * saying so beats "Request failed".
 */
export function ErrorMessage({ error }: { error: unknown }): JSX.Element | null {
  if (!error) return null;

  if (error instanceof ApiError) {
    if (error.isConflict) {
      return (
        <div className="alert alert--warning">
          Someone else changed this ticket while you were working on it. Reload to see their
          changes, then try again.
        </div>
      );
    }
    if (error.isRuleViolation) {
      const allowed = error.details?.['allowed'];
      return (
        <div className="alert alert--warning">
          {error.message}
          {Array.isArray(allowed) && allowed.length > 0 ? (
            <div className="subtle" style={{ marginTop: 4 }}>
              Available from here: {allowed.join(', ')}
            </div>
          ) : null}
        </div>
      );
    }
    return (
      <div className="alert alert--error">
        {error.message}
        {/* The request id is what turns "it broke" into a log line someone can
            actually find. */}
        {error.requestId ? (
          <div className="subtle mono" style={{ marginTop: 4 }}>
            Reference: {error.requestId}
          </div>
        ) : null}
      </div>
    );
  }

  return <div className="alert alert--error">Something went wrong. Please try again.</div>;
}

export function Field({
  label,
  hint,
  children,
  htmlFor,
}: {
  label: string;
  hint?: string;
  children: ReactNode;
  htmlFor?: string;
}): JSX.Element {
  return (
    <div className="field">
      <label className="field__label" htmlFor={htmlFor}>
        {label}
      </label>
      {children}
      {hint ? <div className="field__hint">{hint}</div> : null}
    </div>
  );
}

export function Card({
  title,
  action,
  children,
}: {
  title?: string;
  action?: ReactNode;
  children: ReactNode;
}): JSX.Element {
  return (
    <section className="card">
      {title || action ? (
        <header className="card__header">
          {title ? <h3 className="card__title">{title}</h3> : <span />}
          {action}
        </header>
      ) : null}
      <div className="card__body">{children}</div>
    </section>
  );
}
