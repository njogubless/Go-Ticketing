import { useState, type FormEvent } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import { Permission, useAuth } from '@/auth/AuthContext';
import { Card, EmptyState, ErrorMessage, Field, Spinner } from '@/components/primitives';
import { duration } from '@/lib/format';

/**
 * Organisation administration: people, teams, and the SLA policies in force.
 *
 * Accounts are created here, by an admin, with a role chosen by that admin.
 * There is no path by which somebody assigns themselves a role — the signup
 * form does not accept one and the server ignores it if sent.
 */
export function AdminPage(): JSX.Element {
  const { can } = useAuth();

  return (
    <>
      <header className="main__header">
        <div>
          <h1>Administration</h1>
          <div className="subtle">People, teams and service levels</div>
        </div>
      </header>

      <div className="main__body">
        <div className="stack">
          {can(Permission.UserManage) ? <UsersPanel /> : null}
          {can(Permission.TeamManage) ? <TeamsPanel /> : null}
          {can(Permission.SLAManage) ? <SLAPanel /> : null}
        </div>
      </div>
    </>
  );
}

function UsersPanel(): JSX.Element {
  const queryClient = useQueryClient();
  const [adding, setAdding] = useState(false);
  const [form, setForm] = useState({
    email: '',
    full_name: '',
    password: '',
    role: 'agent',
    team_id: '',
  });

  const usersQuery = useQuery({ queryKey: ['users'], queryFn: () => api.listUsers() });
  const teamsQuery = useQuery({ queryKey: ['teams'], queryFn: () => api.listTeams() });

  const invite = useMutation({
    mutationFn: () =>
      api.inviteUser({
        email: form.email,
        full_name: form.full_name,
        password: form.password,
        role: form.role,
        ...(form.team_id ? { team_id: form.team_id } : {}),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['users'] });
      setAdding(false);
      setForm({ email: '', full_name: '', password: '', role: 'agent', team_id: '' });
    },
  });

  const teamName = (teamId: string | undefined): string => {
    if (!teamId) return '—';
    return teamsQuery.data?.items.find((team) => team.id === teamId)?.name ?? '—';
  };

  return (
    <Card
      title="People"
      action={
        <button type="button" className="button button--sm" onClick={() => setAdding(!adding)}>
          {adding ? 'Cancel' : 'Add person'}
        </button>
      }
    >
      {adding ? (
        <form
          onSubmit={(event: FormEvent) => {
            event.preventDefault();
            invite.mutate();
          }}
          style={{ marginBottom: 20 }}
          noValidate
        >
          <ErrorMessage error={invite.error} />

          <div className="field__row">
            <Field label="Full name" htmlFor="new-name">
              <input
                id="new-name"
                className="input"
                value={form.full_name}
                onChange={(event) => setForm({ ...form, full_name: event.target.value })}
                required
              />
            </Field>
            <Field label="Email" htmlFor="new-email">
              <input
                id="new-email"
                className="input"
                type="email"
                value={form.email}
                onChange={(event) => setForm({ ...form, email: event.target.value })}
                required
              />
            </Field>
          </div>

          <div className="field__row">
            <Field label="Role" htmlFor="new-role">
              <select
                id="new-role"
                className="select"
                value={form.role}
                onChange={(event) => setForm({ ...form, role: event.target.value })}
              >
                <option value="requester">Requester — raises tickets</option>
                <option value="agent">Agent — works a queue</option>
                <option value="manager">Manager — oversees teams, approves changes</option>
                <option value="admin">Admin — full administration</option>
              </select>
            </Field>
            <Field label="Team" htmlFor="new-team">
              <select
                id="new-team"
                className="select"
                value={form.team_id}
                onChange={(event) => setForm({ ...form, team_id: event.target.value })}
              >
                <option value="">No team</option>
                {teamsQuery.data?.items.map((team) => (
                  <option key={team.id} value={team.id}>
                    {team.name}
                  </option>
                ))}
              </select>
            </Field>
          </div>

          <Field
            label="Initial password"
            htmlFor="new-password"
            hint="At least 12 characters. Share it securely and ask them to change it."
          >
            <input
              id="new-password"
              className="input"
              type="text"
              value={form.password}
              onChange={(event) => setForm({ ...form, password: event.target.value })}
              minLength={12}
              required
            />
          </Field>

          <button type="submit" className="button button--primary" disabled={invite.isPending}>
            {invite.isPending ? 'Creating…' : 'Create account'}
          </button>
        </form>
      ) : null}

      {usersQuery.isLoading ? (
        <Spinner label="Loading people" />
      ) : (
        <table className="table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Email</th>
              <th>Role</th>
              <th>Team</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            {usersQuery.data?.items.map((user) => (
              <tr key={user.id}>
                <td>{user.full_name}</td>
                <td className="muted">{user.email}</td>
                <td>
                  <span className="badge badge--neutral">{user.role}</span>
                </td>
                <td className="muted">{teamName(user.team_id)}</td>
                <td>
                  <span className={`badge ${user.active ? 'badge--success' : 'badge--neutral'}`}>
                    {user.active ? 'active' : 'disabled'}
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}

function TeamsPanel(): JSX.Element {
  const queryClient = useQueryClient();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');

  const teamsQuery = useQuery({ queryKey: ['teams'], queryFn: () => api.listTeams() });

  const create = useMutation({
    mutationFn: () => api.createTeam(name, description),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['teams'] });
      setName('');
      setDescription('');
    },
  });

  return (
    <Card title="Teams">
      <ErrorMessage error={create.error} />

      <form
        className="row"
        style={{ marginBottom: 16 }}
        onSubmit={(event: FormEvent) => {
          event.preventDefault();
          if (name.trim()) create.mutate();
        }}
      >
        <input
          className="input"
          placeholder="Team name"
          value={name}
          onChange={(event) => setName(event.target.value)}
          aria-label="Team name"
        />
        <input
          className="input"
          placeholder="Description"
          value={description}
          onChange={(event) => setDescription(event.target.value)}
          aria-label="Team description"
        />
        <button type="submit" className="button button--primary" disabled={create.isPending}>
          Add
        </button>
      </form>

      {teamsQuery.isLoading ? (
        <Spinner label="Loading teams" />
      ) : (teamsQuery.data?.items.length ?? 0) === 0 ? (
        <EmptyState title="No teams yet" description="Teams are the queues agents work from." />
      ) : (
        <table className="table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Description</th>
              <th>Escalates to</th>
            </tr>
          </thead>
          <tbody>
            {teamsQuery.data?.items.map((team) => (
              <tr key={team.id}>
                <td>{team.name}</td>
                <td className="muted">{team.description || '—'}</td>
                <td className="muted">
                  {team.escalates_to
                    ? (teamsQuery.data?.items.find((other) => other.id === team.escalates_to)?.name ??
                      '—')
                    : '—'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}

function SLAPanel(): JSX.Element {
  const slaQuery = useQuery({ queryKey: ['sla'], queryFn: () => api.listSLA() });

  const calendarName = (calendarId: string): string =>
    slaQuery.data?.calendars.find((calendar) => calendar.id === calendarId)?.name ?? '—';

  return (
    <Card title="Service level targets">
      {slaQuery.isLoading ? (
        <Spinner label="Loading policies" />
      ) : (
        <>
          <table className="table">
            <thead>
              <tr>
                <th>Policy</th>
                <th>Applies to</th>
                <th>First response</th>
                <th>Resolution</th>
                <th>Calendar</th>
              </tr>
            </thead>
            <tbody>
              {slaQuery.data?.policies.map((policy) => (
                <tr key={policy.id}>
                  <td>{policy.name}</td>
                  <td className="muted">
                    {policy.priority ?? 'any priority'}
                    {policy.kind ? ` · ${policy.kind}` : ''}
                  </td>
                  <td>{duration(policy.first_response_seconds)}</td>
                  <td>{duration(policy.resolution_seconds)}</td>
                  <td className="muted">{calendarName(policy.calendar_id)}</td>
                </tr>
              ))}
            </tbody>
          </table>

          <div className="subtle" style={{ marginTop: 12 }}>
            Targets are measured in working time against the policy&apos;s calendar. A four-hour
            target raised at 16:00 on a Friday is due mid-morning on Monday, not at 20:00 that
            evening.
          </div>
        </>
      )}
    </Card>
  );
}
