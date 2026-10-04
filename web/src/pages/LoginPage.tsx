import { useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { useAuth } from '@/auth/AuthContext';
import { ErrorMessage, Field } from '@/components/primitives';

export function LoginPage(): JSX.Element {
  const { login } = useAuth();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<unknown>(null);
  const [submitting, setSubmitting] = useState(false);

  const handleSubmit = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      await login(email, password);
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
          <h1 className="auth__title">Sign in</h1>
          <p className="auth__subtitle">Service Desk</p>

          <ErrorMessage error={error} />

          <form onSubmit={handleSubmit} noValidate>
            <Field label="Email" htmlFor="email">
              <input
                id="email"
                className="input"
                type="email"
                value={email}
                onChange={(event) => setEmail(event.target.value)}
                autoComplete="username"
                required
              />
            </Field>

            <Field label="Password" htmlFor="password">
              <input
                id="password"
                className="input"
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                autoComplete="current-password"
                required
              />
            </Field>

            <button
              type="submit"
              className="button button--primary"
              style={{ width: '100%' }}
              disabled={submitting}
            >
              {submitting ? 'Signing in…' : 'Sign in'}
            </button>
          </form>

          <div className="auth__switch">
            New organisation? <Link to="/register">Create one</Link>
          </div>
        </div>
      </div>
    </div>
  );
}
