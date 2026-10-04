import type { ReactNode } from 'react';
import { NavLink, useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { Permission, useAuth, useIsStaff } from '@/auth/AuthContext';

/**
 * The application shell: navigation, identity, sign-out.
 *
 * Nav items are filtered by permission. That is a usability decision — the
 * server rejects the same calls regardless — but showing a requester an
 * "Assets" link they will only be refused at is worse than not showing it.
 */
export function Shell({ children }: { children: ReactNode }): JSX.Element {
  const { user, can, logout } = useAuth();
  const isStaff = useIsStaff();
  const navigate = useNavigate();

  // Badge the approvals link with a count, so a change waiting on this person
  // is visible without them going looking. Approvals stalling because nobody
  // knew they were asked is the most common failure of a change process.
  const approvalsQuery = useQuery({
    queryKey: ['approvals', 'inbox'],
    queryFn: () => api.approvalInbox(),
    enabled: can(Permission.ApprovalDecide),
    refetchInterval: 120_000,
  });
  const pendingApprovals = approvalsQuery.data?.items.length ?? 0;

  const handleSignOut = async (): Promise<void> => {
    await logout();
    navigate('/login', { replace: true });
  };

  const linkClass = ({ isActive }: { isActive: boolean }): string =>
    `sidebar__link${isActive ? ' sidebar__link--active' : ''}`;

  return (
    <div className="app-shell">
      <nav className="sidebar" aria-label="Main">
        <div className="sidebar__brand">
          <span className="sidebar__mark" aria-hidden="true">
            SD
          </span>
          Service Desk
        </div>

        <div>
          <div className="sidebar__section-title">Work</div>
          {isStaff ? (
            <NavLink to="/queue" className={linkClass}>
              Queue
            </NavLink>
          ) : null}
          <NavLink to="/my-tickets" className={linkClass}>
            My tickets
          </NavLink>
          {can(Permission.ApprovalDecide) ? (
            <NavLink to="/approvals" className={linkClass}>
              Approvals
              {pendingApprovals > 0 ? (
                <span className="badge badge--warning">{pendingApprovals}</span>
              ) : null}
            </NavLink>
          ) : null}
        </div>

        <div>
          <div className="sidebar__section-title">Service</div>
          <NavLink to="/tickets/new" className={linkClass}>
            Raise a ticket
          </NavLink>
          {can(Permission.AssetRead) ? (
            <NavLink to="/assets" className={linkClass}>
              Assets
            </NavLink>
          ) : null}
          {can(Permission.ReportRead) ? (
            <NavLink to="/reports" className={linkClass}>
              Reports
            </NavLink>
          ) : null}
          {can(Permission.UserManage) || can(Permission.TeamManage) ? (
            <NavLink to="/admin" className={linkClass}>
              Administration
            </NavLink>
          ) : null}
        </div>

        <div className="sidebar__footer">
          <div className="sidebar__user">
            <strong>{user?.full_name}</strong>
            <span className="mono">{user?.role}</span>
          </div>
          <button type="button" className="button button--ghost button--sm" onClick={handleSignOut}>
            Sign out
          </button>
        </div>
      </nav>

      <div className="main">{children}</div>
    </div>
  );
}
