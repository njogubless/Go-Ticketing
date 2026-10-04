import { useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { useMutation, useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { Impact, TicketKind, Urgency } from '@/api/types';
import { Permission, useAuth } from '@/auth/AuthContext';
import { Card, ErrorMessage, Field } from '@/components/primitives';

/**
 * The intake form.
 *
 * This screen is used most often by people who are not IT staff, so it asks
 * for impact and urgency in plain language ("one person" / "work is stopped")
 * rather than in ITIL vocabulary. The priority is derived from those two
 * answers and shown before submitting, so the requester sees the consequence
 * of what they picked — which is also the gentlest way to discourage everyone
 * from selecting the most severe option.
 */
export function NewTicketPage(): JSX.Element {
  const navigate = useNavigate();
  const { can } = useAuth();

  const [form, setForm] = useState({
    kind: 'incident' as TicketKind,
    subject: '',
    description: '',
    category: '',
    impact: 'low' as Impact,
    urgency: 'medium' as Urgency,
    team_id: '',
    asset_id: '',
    tags: '',
  });

  const teamsQuery = useQuery({
    queryKey: ['teams'],
    queryFn: () => api.listTeams(),
  });

  const assetsQuery = useQuery({
    queryKey: ['assets', 'picker'],
    queryFn: () => api.listAssets({ limit: 100 }),
    enabled: can(Permission.AssetRead),
  });

  const create = useMutation({
    mutationFn: () =>
      api.createTicket({
        kind: form.kind,
        subject: form.subject,
        description: form.description,
        impact: form.impact,
        urgency: form.urgency,
        ...(form.category ? { category: form.category } : {}),
        ...(form.team_id ? { team_id: form.team_id } : {}),
        ...(form.asset_id ? { asset_ids: [form.asset_id] } : {}),
        ...(form.tags.trim()
          ? { tags: form.tags.split(',').map((tag) => tag.trim()).filter(Boolean) }
          : {}),
      }),
    onSuccess: (ticket) => navigate(`/tickets/${ticket.id}`),
  });

  const update = (key: keyof typeof form, value: string): void => {
    setForm((previous) => ({ ...previous, [key]: value }));
  };

  const handleSubmit = (event: FormEvent): void => {
    event.preventDefault();
    create.mutate();
  };

  return (
    <>
      <header className="main__header">
        <h1>Raise a ticket</h1>
      </header>

      <div className="main__body">
        <div style={{ maxWidth: 720 }}>
          <ErrorMessage error={create.error} />

          <Card>
            <form onSubmit={handleSubmit} noValidate>
              <Field label="What kind of request is this?" htmlFor="kind">
                <select
                  id="kind"
                  className="select"
                  value={form.kind}
                  onChange={(event) => update('kind', event.target.value)}
                >
                  <option value="incident">Something is broken (incident)</option>
                  <option value="service_request">I need something (service request)</option>
                  {can(Permission.TicketTransition) ? (
                    <>
                      <option value="change">A planned change</option>
                      <option value="problem">An underlying problem</option>
                    </>
                  ) : null}
                </select>
                {form.kind === 'change' ? (
                  <div className="field__hint">
                    Changes must be approved before work can start.
                  </div>
                ) : null}
              </Field>

              <Field label="Summary" htmlFor="subject" hint="One line — what is happening?">
                <input
                  id="subject"
                  className="input"
                  value={form.subject}
                  onChange={(event) => update('subject', event.target.value)}
                  maxLength={200}
                  required
                />
              </Field>

              <Field
                label="Details"
                htmlFor="description"
                hint="When did it start? What have you already tried? Any error messages?"
              >
                <textarea
                  id="description"
                  className="textarea"
                  value={form.description}
                  onChange={(event) => update('description', event.target.value)}
                  rows={6}
                  required
                />
              </Field>

              <div className="field__row">
                <Field label="Who is affected?" htmlFor="impact">
                  <select
                    id="impact"
                    className="select"
                    value={form.impact}
                    onChange={(event) => update('impact', event.target.value)}
                  >
                    <option value="low">Just me</option>
                    <option value="medium">My team or department</option>
                    <option value="high">The whole organisation</option>
                  </select>
                </Field>

                <Field label="How urgent is it?" htmlFor="urgency">
                  <select
                    id="urgency"
                    className="select"
                    value={form.urgency}
                    onChange={(event) => update('urgency', event.target.value)}
                  >
                    <option value="low">I have a workaround</option>
                    <option value="medium">It is slowing me down</option>
                    <option value="high">I cannot work at all</option>
                  </select>
                </Field>
              </div>

              <div className="alert alert--info">
                This will be raised as <strong>{derivePriority(form.impact, form.urgency)}</strong>.
                Priority is calculated from who is affected and how urgent it is.
              </div>

              {can(Permission.AssetRead) && (assetsQuery.data?.items.length ?? 0) > 0 ? (
                <Field
                  label="Related equipment"
                  htmlFor="asset"
                  hint="Optional — helps us spot equipment that keeps failing"
                >
                  <select
                    id="asset"
                    className="select"
                    value={form.asset_id}
                    onChange={(event) => update('asset_id', event.target.value)}
                  >
                    <option value="">None</option>
                    {assetsQuery.data?.items.map((asset) => (
                      <option key={asset.id} value={asset.id}>
                        {asset.tag} — {asset.name}
                      </option>
                    ))}
                  </select>
                </Field>
              ) : null}

              {can(Permission.TicketAssign) ? (
                <div className="field__row">
                  <Field label="Team" htmlFor="team">
                    <select
                      id="team"
                      className="select"
                      value={form.team_id}
                      onChange={(event) => update('team_id', event.target.value)}
                    >
                      <option value="">Unrouted</option>
                      {teamsQuery.data?.items.map((team) => (
                        <option key={team.id} value={team.id}>
                          {team.name}
                        </option>
                      ))}
                    </select>
                  </Field>

                  <Field label="Tags" htmlFor="tags" hint="Comma separated">
                    <input
                      id="tags"
                      className="input"
                      value={form.tags}
                      onChange={(event) => update('tags', event.target.value)}
                    />
                  </Field>
                </div>
              ) : null}

              <div className="row">
                <button type="submit" className="button button--primary" disabled={create.isPending}>
                  {create.isPending ? 'Submitting…' : 'Submit ticket'}
                </button>
                <button type="button" className="button button--ghost" onClick={() => navigate(-1)}>
                  Cancel
                </button>
              </div>
            </form>
          </Card>
        </div>
      </div>
    </>
  );
}

function derivePriority(impact: Impact, urgency: Urgency): string {
  const matrix: Record<Impact, Record<Urgency, string>> = {
    high: { high: 'P1', medium: 'P2', low: 'P3' },
    medium: { high: 'P2', medium: 'P3', low: 'P4' },
    low: { high: 'P3', medium: 'P4', low: 'P4' },
  };
  return matrix[impact][urgency];
}
