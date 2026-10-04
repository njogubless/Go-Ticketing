import { Suspense, lazy } from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';
import { useAuth, useIsStaff } from '@/auth/AuthContext';
import { useRealtime } from '@/realtime/useRealtime';
import { Shell } from '@/components/Shell';
import { Spinner } from '@/components/primitives';
import { LoginPage } from '@/pages/LoginPage';
import { RegisterPage } from '@/pages/RegisterPage';
import { QueuePage } from '@/pages/QueuePage';
import { TicketPage } from '@/pages/TicketPage';
import { NewTicketPage } from '@/pages/NewTicketPage';
import { ApprovalsPage } from '@/pages/ApprovalsPage';
import { AssetsPage } from '@/pages/AssetsPage';
import { AdminPage } from '@/pages/AdminPage';
import { MyTicketsPage } from '@/pages/MyTicketsPage';

/**
 * Reports is the only route that needs a charting library, and that library is
 * larger than the entire rest of the application. Loading it lazily means an
 * agent working the queue never downloads it — they are the majority of
 * sessions, and it is the majority of the bundle.
 */
const ReportsPage = lazy(() =>
  import('@/pages/ReportsPage').then((module) => ({ default: module.ReportsPage })),
);

export function App(): JSX.Element {
  const { user, loading } = useAuth();
  const isStaff = useIsStaff();

  // The realtime connection is opened once, here, for the whole session —
  // rather than per page. A socket per route would reconnect on every
  // navigation, and reconnect storms are exactly what the backoff in
  // useRealtime exists to avoid.
  useRealtime(user !== null);

  if (loading) {
    return (
      <div className="auth">
        <Spinner label="Restoring your session" />
      </div>
    );
  }

  if (!user) {
    return (
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/register" element={<RegisterPage />} />
        {/* Any other path while signed out lands on login rather than a 404 —
            a bookmarked ticket URL should survive a session expiring. */}
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    );
  }

  return (
    <Shell>
      <Routes>
        {/* Staff land on the queue; requesters land on their own tickets.
            Same app, different centre of gravity. */}
        <Route path="/" element={<Navigate to={isStaff ? '/queue' : '/my-tickets'} replace />} />

        <Route path="/my-tickets" element={<MyTicketsPage />} />
        <Route path="/tickets/new" element={<NewTicketPage />} />
        <Route path="/tickets/:id" element={<TicketPage />} />

        {isStaff ? <Route path="/queue" element={<QueuePage />} /> : null}
        {isStaff ? <Route path="/approvals" element={<ApprovalsPage />} /> : null}
        {isStaff ? <Route path="/assets" element={<AssetsPage />} /> : null}
        <Route
          path="/reports"
          element={
            <Suspense
              fallback={
                <div className="main__body">
                  <Spinner label="Loading reports" />
                </div>
              }
            >
              <ReportsPage />
            </Suspense>
          }
        />
        <Route path="/admin" element={<AdminPage />} />

        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Shell>
  );
}
