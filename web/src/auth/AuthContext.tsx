import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react';
import { api, setSessionExpiredHandler, tokenStore } from '@/api/client';
import type { MeResponse, User } from '@/api/types';

/**
 * Permission strings, mirroring internal/domain/identity/role.go.
 *
 * The frontend uses these only to decide what to *show*. Every one of them is
 * enforced again server-side — hiding a button is a usability decision, not a
 * security control, and this file would be worthless as one because anyone can
 * edit it in their own browser.
 */
export const Permission = {
  TicketCreate: 'ticket:create',
  TicketReadTeam: 'ticket:read_team',
  TicketReadAll: 'ticket:read_all',
  TicketTransition: 'ticket:transition',
  TicketAssign: 'ticket:assign',
  NoteInternal: 'ticket:note_internal',
  AuditRead: 'audit:read',
  ApprovalDecide: 'approval:decide',
  AssetRead: 'asset:read',
  AssetManage: 'asset:manage',
  SLAManage: 'sla:manage',
  ReportRead: 'report:read',
  UserManage: 'user:manage',
  TeamManage: 'team:manage',
} as const;

export type PermissionValue = (typeof Permission)[keyof typeof Permission];

interface AuthState {
  user: User | null;
  permissions: Set<string>;
  /** True until the initial session restore settles, so routes don't flash. */
  loading: boolean;
  can: (permission: PermissionValue) => boolean;
  login: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  registerOrganization: (input: RegisterInput) => Promise<void>;
}

export interface RegisterInput {
  organization_name: string;
  organization_slug: string;
  ticket_prefix: string;
  timezone: string;
  full_name: string;
  email: string;
  password: string;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }): JSX.Element {
  const [user, setUser] = useState<User | null>(null);
  const [permissions, setPermissions] = useState<Set<string>>(new Set());
  const [loading, setLoading] = useState(true);

  const applySession = useCallback((me: MeResponse) => {
    setUser(me.user);
    setPermissions(new Set(me.permissions));
  }, []);

  const clearSession = useCallback(() => {
    setUser(null);
    setPermissions(new Set());
  }, []);

  // Restore the session on mount. Without this, every page refresh drops the
  // user at the login screen even though their refresh token is still valid.
  useEffect(() => {
    let cancelled = false;

    void (async () => {
      if (!tokenStore.hasSession()) {
        if (!cancelled) setLoading(false);
        return;
      }
      const me = await api.restoreSession();
      if (cancelled) return;
      if (me) applySession(me);
      else clearSession();
      setLoading(false);
    })();

    return () => {
      cancelled = true;
    };
  }, [applySession, clearSession]);

  // The API client calls this when a refresh fails, so an expired session
  // drops to the login screen instead of leaving a shell rendering 401s.
  useEffect(() => {
    setSessionExpiredHandler(clearSession);
  }, [clearSession]);

  const login = useCallback(
    async (email: string, password: string) => {
      const auth = await api.login(email, password);
      const me = await api.me();
      applySession(me);
      // Belt and braces: the login response carries the user too, and using it
      // avoids a flash of empty state if /me is momentarily slow.
      setUser(auth.user);
    },
    [applySession],
  );

  const registerOrganization = useCallback(
    async (input: RegisterInput) => {
      await api.registerOrganization(input);
      applySession(await api.me());
    },
    [applySession],
  );

  const logout = useCallback(async () => {
    await api.logout();
    clearSession();
  }, [clearSession]);

  const can = useCallback(
    (permission: PermissionValue) => permissions.has(permission),
    [permissions],
  );

  const value = useMemo<AuthState>(
    () => ({ user, permissions, loading, can, login, logout, registerOrganization }),
    [user, permissions, loading, can, login, logout, registerOrganization],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const context = useContext(AuthContext);
  if (!context) {
    // A hook used outside its provider is a wiring bug; throwing here turns a
    // confusing runtime null into an immediate, locatable error.
    throw new Error('useAuth must be used inside an AuthProvider');
  }
  return context;
}

/** True when the signed-in user is service-desk staff rather than a requester. */
export function useIsStaff(): boolean {
  const { can } = useAuth();
  return can(Permission.TicketReadTeam);
}
