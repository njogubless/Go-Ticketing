import { useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { useAuth } from '@/auth/AuthContext';
import { ErrorMessage, Field } from '@/components/primitives';

/**
 * Self-service tenant signup: creates the organisation and its first
 * administrator in one step.
 *
 * There is no role selector, and there must never be one. Anyone can reach
 * this form, and a role field here would be a one-click path to minting an
 * administrator of someone else's organisation. Every other account is created
 * by an admin from the Administration screen.
 */
export function RegisterPage(): JSX.Element {
  const { registerOrganization } = useAuth();
  const [form, setForm] = useState({
    organization_name: '',
    organization_slug: '',
    ticket_prefix: '',
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
    full_name: '',
    email: '',
    password: '',
  });
  const [error, setError] = useState<unknown>(null);
  const [submitting, setSubmitting] = useState(false);

  const update = (key: keyof typeof form) => (value: string) => {
    setForm((previous) => ({ ...previous, [key]: value }));
  };

  /** Derives a slug and ticket prefix from the organisation name. */
  const handleNameChange = (value: string): void => {
    setForm((previous) => {
      const autoSlug = value
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, '-')
        .replace(/^-+|-+$/g, '')
        .slice(0, 40);
      const autoPrefix = value
        .toUpperCase()
        .replace(/[^A-Z0-9]/g, '')
        .slice(0, 4);
      return {
        ...previous,
        organization_name: value,
        // Only autofill while the user has not typed their own — overwriting a
        // deliberate choice on every keystroke is maddening.
        organization_slug: previous.organization_slug === '' ? autoSlug : previous.organization_slug,
        ticket_prefix: previous.ticket_prefix === '' ? autoPrefix : previous.ticket_prefix,
      };
    });
  };

  const handleSubmit = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      await registerOrganization(form);
    } catch (caught) {
      setError(caught);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="auth">
      <div className="auth__card card">
        <div className="card__body">
          <h1 className="auth__title">Create your organisation</h1>
          <p className="auth__subtitle">You will be its first administrator.</p>

          <ErrorMessage error={error} />

          <form onSubmit={handleSubmit} noValidate>
            <Field label="Organisation name" htmlFor="org-name">
              <input
                id="org-name"
                className="input"
                value={form.organization_name}
                onChange={(event) => handleNameChange(event.target.value)}
                required
              />
            </Field>

            <div className="field__row">
              <Field label="URL slug" htmlFor="org-slug" hint="Lowercase letters, digits, hyphens">
                <input
                  id="org-slug"
                  className="input mono"
                  value={form.organization_slug}
                  onChange={(event) => update('organization_slug')(event.target.value)}
                  required
                />
              </Field>

              <Field label="Ticket prefix" htmlFor="org-prefix" hint="e.g. ACME-1042">
                <input
                  id="org-prefix"
                  className="input mono"
                  value={form.ticket_prefix}
                  onChange={(event) => update('ticket_prefix')(event.target.value.toUpperCase())}
                  required
                />
              </Field>
            </div>

            <Field
              label="Timezone"
              htmlFor="org-tz"
              hint="Used for business-hours SLA calculations"
            >
              <input
                id="org-tz"
                className="input mono"
                value={form.timezone}
                onChange={(event) => update('timezone')(event.target.value)}
                required
              />
            </Field>

            <Field label="Your name" htmlFor="admin-name">
              <input
                id="admin-name"
                className="input"
                value={form.full_name}
                onChange={(event) => update('full_name')(event.target.value)}
                autoComplete="name"
                required
              />
            </Field>

            <Field label="Email" htmlFor="admin-email">
              <input
                id="admin-email"
                className="input"
                type="email"
                value={form.email}
                onChange={(event) => update('email')(event.target.value)}
                autoComplete="username"
                required
              />
            </Field>

            <Field
              label="Password"
              htmlFor="admin-password"
              hint="At least 12 characters — a passphrase is easiest"
            >
              <input
                id="admin-password"
                className="input"
                type="password"
                value={form.password}
                onChange={(event) => update('password')(event.target.value)}
                autoComplete="new-password"
                minLength={12}
                required
              />
            </Field>

            <button
              type="submit"
              className="button button--primary"
              style={{ width: '100%' }}
              disabled={submitting}
            >
              {submitting ? 'Creating…' : 'Create organisation'}
            </button>
          </form>

          <div className="auth__switch">
            Already have an account? <Link to="/login">Sign in</Link>
          </div>
        </div>
      </div>
    </div>
  );
}
