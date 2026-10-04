import type {
  ApiErrorBody,
  Approval,
  Asset,
  AuditEntry,
  AuthResponse,
  MeResponse,
  Page,
  SLACalendar,
  SLAPolicy,
  SavedView,
  Stats,
  Message,
  Team,
  TicketDetail,
  TicketFilters,
  TicketSummary,
  User,
  Visibility,
} from './types';

const API_BASE = '/api/v1';

/**
 * ApiError carries the server's stable error code alongside the HTTP status.
 *
 * Components branch on `code`, never on the message. Messages are written for
 * humans and will be reworded; codes are a contract. Anything that branches on
 * prose breaks the first time someone improves the copy.
 */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly details?: Record<string, unknown>,
    readonly requestId?: string,
  ) {
    super(message);
    this.name = 'ApiError';
  }

  /** A stale-write conflict — someone else changed the ticket first. */
  get isConflict(): boolean {
    return this.status === 409;
  }

  /** The state machine refused: well-formed request, wrong ticket state. */
  get isRuleViolation(): boolean {
    return this.status === 422;
  }

  get isUnauthorized(): boolean {
    return this.status === 401;
  }
}

/**
 * Token storage.
 *
 * The access token is held in memory only. Putting it in localStorage would
 * make it readable by any XSS payload that ever lands on the page, and it
 * would survive a tab close.
 *
 * The refresh token does go to localStorage, which is a deliberate trade-off
 * rather than an oversight: without it a page reload logs the user out, which
 * is unusable. It is single-use and rotates on every refresh, so a stolen one
 * is detected the moment the legitimate client next refreshes and the whole
 * family is revoked server-side.
 *
 * The genuinely correct answer is a httpOnly, SameSite=Strict refresh cookie —
 * recorded in docs/adr/0006 as the upgrade to make before a public launch.
 */
const REFRESH_TOKEN_KEY = 'servicedesk.refresh';

let accessToken: string | null = null;
let accessTokenExpiry = 0;

export const tokenStore = {
  setSession(auth: AuthResponse): void {
    accessToken = auth.access_token;
    accessTokenExpiry = new Date(auth.expires_at).getTime();
    localStorage.setItem(REFRESH_TOKEN_KEY, auth.refresh_token);
  },
  clear(): void {
    accessToken = null;
    accessTokenExpiry = 0;
    localStorage.removeItem(REFRESH_TOKEN_KEY);
  },
  getRefreshToken(): string | null {
    return localStorage.getItem(REFRESH_TOKEN_KEY);
  },
  hasSession(): boolean {
    return accessToken !== null || localStorage.getItem(REFRESH_TOKEN_KEY) !== null;
  },
};

/** Called when refreshing fails, so the app can drop to the login screen. */
let onSessionExpired: (() => void) | null = null;
export function setSessionExpiredHandler(handler: () => void): void {
  onSessionExpired = handler;
}

/**
 * A single in-flight refresh, shared by every caller.
 *
 * Without this, a dashboard that fires six requests on mount would trigger six
 * concurrent refreshes on an expired token. Refresh tokens are single-use, so
 * five of those would present an already-consumed token — which the server
 * correctly reads as a stolen token and responds to by revoking the entire
 * family. The user would be logged out by their own dashboard loading.
 */
let refreshInFlight: Promise<boolean> | null = null;

async function refreshAccessToken(): Promise<boolean> {
  if (refreshInFlight) return refreshInFlight;

  refreshInFlight = (async () => {
    const refreshToken = tokenStore.getRefreshToken();
    if (!refreshToken) return false;

    try {
      const response = await fetch(`${API_BASE}/auth/refresh`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refresh_token: refreshToken }),
      });
      if (!response.ok) {
        tokenStore.clear();
        return false;
      }
      tokenStore.setSession((await response.json()) as AuthResponse);
      return true;
    } catch {
      return false;
    } finally {
      refreshInFlight = null;
    }
  })();

  return refreshInFlight;
}

interface RequestOptions {
  method?: string;
  body?: unknown;
  /** Optimistic-lock token, echoed from a prior read's ETag. */
  ifMatch?: string;
  signal?: AbortSignal;
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { response, body } = await sendWithRetry<T>(path, options);

  if (!response.ok) {
    const errorBody = body as ApiErrorBody | undefined;
    throw new ApiError(
      response.status,
      errorBody?.error?.code ?? 'unknown',
      errorBody?.error?.message ?? `Request failed with status ${response.status}`,
      errorBody?.error?.details,
      errorBody?.error?.request_id,
    );
  }

  return body as T;
}

/**
 * Sends the request, and on a 401 refreshes once and replays it.
 *
 * Exactly one retry: a refresh that yields a token the server also rejects
 * means the session is genuinely gone, and retrying further would spin.
 */
async function sendWithRetry<T>(
  path: string,
  options: RequestOptions,
): Promise<{ response: Response; body: T | ApiErrorBody | undefined }> {
  let response = await send(path, options);

  if (response.status === 401 && tokenStore.getRefreshToken()) {
    if (await refreshAccessToken()) {
      response = await send(path, options);
    } else {
      onSessionExpired?.();
    }
  }

  // 204 No Content has no body; calling .json() on it throws.
  if (response.status === 204) {
    return { response, body: undefined };
  }

  let body: T | ApiErrorBody | undefined;
  try {
    body = (await response.json()) as T | ApiErrorBody;
  } catch {
    body = undefined;
  }
  return { response, body };
}

async function send(path: string, options: RequestOptions): Promise<Response> {
  const headers: Record<string, string> = {};
  if (options.body !== undefined) headers['Content-Type'] = 'application/json';
  if (accessToken) headers['Authorization'] = `Bearer ${accessToken}`;
  if (options.ifMatch) headers['If-Match'] = options.ifMatch;

  const init: RequestInit = { method: options.method ?? 'GET', headers };
  if (options.body !== undefined) init.body = JSON.stringify(options.body);
  if (options.signal) init.signal = options.signal;

  return fetch(`${API_BASE}${path}`, init);
}

/** Reads the ETag alongside the body, for endpoints that need the lock token. */
async function requestWithETag<T>(path: string): Promise<{ data: T; etag: string | null }> {
  const response = await send(path, {});
  if (response.status === 401 && (await refreshAccessToken())) {
    return requestWithETag<T>(path);
  }
  const body = (await response.json()) as T | ApiErrorBody;
  if (!response.ok) {
    const errorBody = body as ApiErrorBody;
    throw new ApiError(
      response.status,
      errorBody?.error?.code ?? 'unknown',
      errorBody?.error?.message ?? 'Request failed',
      errorBody?.error?.details,
      errorBody?.error?.request_id,
    );
  }
  return { data: body as T, etag: response.headers.get('ETag') };
}

/** Builds a query string, dropping empty values so URLs stay readable. */
function toQuery(filters: TicketFilters & { cursor?: string; limit?: number }): string {
  const params = new URLSearchParams();
  const append = (key: string, value: unknown): void => {
    if (value === undefined || value === null || value === '') return;
    if (Array.isArray(value)) {
      if (value.length > 0) params.set(key, value.join(','));
      return;
    }
    if (typeof value === 'boolean') {
      if (value) params.set(key, 'true');
      return;
    }
    params.set(key, String(value));
  };

  append('status', filters.status);
  append('priority', filters.priority);
  append('kind', filters.kind);
  append('team_id', filters.team_id);
  append('tags', filters.tags);
  append('assignee_id', filters.assignee_id);
  append('requester_id', filters.requester_id);
  append('asset_id', filters.asset_id);
  append('unassigned', filters.unassigned);
  append('breached', filters.breached);
  append('q', filters.q);
  append('sort', filters.sort);
  append('cursor', filters.cursor);
  append('limit', filters.limit);

  const query = params.toString();
  return query ? `?${query}` : '';
}

export const api = {
  // --- auth ---------------------------------------------------------------
  async login(email: string, password: string): Promise<AuthResponse> {
    const auth = await request<AuthResponse>('/auth/login', {
      method: 'POST',
      body: { email, password },
    });
    tokenStore.setSession(auth);
    return auth;
  },

  async registerOrganization(input: {
    organization_name: string;
    organization_slug: string;
    ticket_prefix: string;
    timezone: string;
    full_name: string;
    email: string;
    password: string;
  }): Promise<AuthResponse> {
    const auth = await request<AuthResponse>('/auth/register-organization', {
      method: 'POST',
      body: input,
    });
    tokenStore.setSession(auth);
    return auth;
  },

  async logout(): Promise<void> {
    const refreshToken = tokenStore.getRefreshToken();
    if (refreshToken) {
      // Best-effort: the local session is cleared regardless, so a failed
      // network call cannot leave the user apparently still signed in.
      await request<void>('/auth/logout', {
        method: 'POST',
        body: { refresh_token: refreshToken },
      }).catch(() => undefined);
    }
    tokenStore.clear();
  },

  /** Restores a session on page load using the stored refresh token. */
  async restoreSession(): Promise<MeResponse | null> {
    if (!tokenStore.getRefreshToken()) return null;
    if (!accessToken || Date.now() >= accessTokenExpiry) {
      if (!(await refreshAccessToken())) return null;
    }
    try {
      return await request<MeResponse>('/me');
    } catch {
      tokenStore.clear();
      return null;
    }
  },

  me: () => request<MeResponse>('/me'),

  realtimeTicket: () => request<{ ticket: string }>('/me/realtime-ticket', { method: 'POST' }),

  // --- tickets ------------------------------------------------------------
  listTickets: (filters: TicketFilters & { cursor?: string; limit?: number } = {}) =>
    request<Page<TicketSummary>>(`/tickets${toQuery(filters)}`),

  getTicket: (id: string) => requestWithETag<TicketDetail>(`/tickets/${id}`),

  getTicketByReference: (reference: string) =>
    request<TicketDetail>(`/tickets-by-reference/${encodeURIComponent(reference)}`),

  createTicket: (input: {
    kind: string;
    subject: string;
    description: string;
    impact: string;
    urgency: string;
    category?: string;
    tags?: string[];
    team_id?: string;
    asset_ids?: string[];
    on_behalf_of?: string;
  }) => request<TicketDetail>('/tickets', { method: 'POST', body: input }),

  transitionTicket: (id: string, status: string, resolution: string | undefined, etag: string | null) =>
    request<TicketDetail>(`/tickets/${id}/status`, {
      method: 'PATCH',
      body: resolution ? { status, resolution } : { status },
      ...(etag ? { ifMatch: etag } : {}),
    }),

  assignTicket: (id: string, assigneeId: string | null, etag: string | null) =>
    request<TicketDetail>(`/tickets/${id}/assignee`, {
      method: 'PATCH',
      body: { assignee_id: assigneeId },
      ...(etag ? { ifMatch: etag } : {}),
    }),

  routeTicket: (id: string, teamId: string | null, etag: string | null) =>
    request<TicketDetail>(`/tickets/${id}/team`, {
      method: 'PATCH',
      body: { team_id: teamId },
      ...(etag ? { ifMatch: etag } : {}),
    }),

  reclassifyTicket: (id: string, impact: string, urgency: string, etag: string | null) =>
    request<TicketDetail>(`/tickets/${id}/classification`, {
      method: 'PATCH',
      body: { impact, urgency },
      ...(etag ? { ifMatch: etag } : {}),
    }),

  addMessage: (id: string, body: string, visibility: Visibility) =>
    request<Message>(`/tickets/${id}/messages`, { method: 'POST', body: { body, visibility } }),

  linkAsset: (ticketId: string, assetId: string) =>
    request<void>(`/tickets/${ticketId}/assets/${assetId}`, { method: 'PUT' }),

  unlinkAsset: (ticketId: string, assetId: string) =>
    request<void>(`/tickets/${ticketId}/assets/${assetId}`, { method: 'DELETE' }),

  auditTrail: (id: string) => request<Page<AuditEntry>>(`/tickets/${id}/audit`),

  // --- approvals ----------------------------------------------------------
  requestApproval: (ticketId: string, approverIds: string[]) =>
    request<{ items: Approval[] }>(`/tickets/${ticketId}/approvals`, {
      method: 'POST',
      body: { approver_ids: approverIds },
    }),

  decideApproval: (approvalId: string, decision: 'approved' | 'rejected', comment: string) =>
    request<Approval>(`/approvals/${approvalId}`, {
      method: 'PATCH',
      body: { decision, comment },
    }),

  approvalInbox: () => request<{ items: Approval[] }>('/approvals/inbox'),

  // --- assets -------------------------------------------------------------
  listAssets: (params: { q?: string; kind?: string; status?: string; limit?: number } = {}) => {
    const query = new URLSearchParams();
    Object.entries(params).forEach(([key, value]) => {
      if (value !== undefined && value !== '') query.set(key, String(value));
    });
    const suffix = query.toString();
    return request<Page<Asset>>(`/assets${suffix ? `?${suffix}` : ''}`);
  },

  assetHotspots: (days = 30) =>
    request<{ items: Array<{ asset: Asset; incident_count: number }>; window_days: number }>(
      `/assets/hotspots?days=${days}`,
    ),

  // --- views, reports, admin ----------------------------------------------
  listViews: () => request<{ items: SavedView[] }>('/views'),

  createView: (name: string, shared: boolean, filters: TicketFilters) =>
    request<SavedView>(`/views${toQuery(filters)}`, { method: 'POST', body: { name, shared } }),

  deleteView: (id: string) => request<void>(`/views/${id}`, { method: 'DELETE' }),

  reportOverview: (params: { from?: string; to?: string; team_id?: string } = {}) => {
    const query = new URLSearchParams();
    Object.entries(params).forEach(([key, value]) => {
      if (value) query.set(key, value);
    });
    const suffix = query.toString();
    return request<Stats>(`/reports/overview${suffix ? `?${suffix}` : ''}`);
  },

  listSLA: () => request<{ policies: SLAPolicy[]; calendars: SLACalendar[] }>('/sla'),

  listUsers: (params: { role?: string; team_id?: string; q?: string } = {}) => {
    const query = new URLSearchParams();
    Object.entries(params).forEach(([key, value]) => {
      if (value) query.set(key, value);
    });
    const suffix = query.toString();
    return request<{ items: User[] }>(`/admin/users${suffix ? `?${suffix}` : ''}`);
  },

  listTeams: () => request<{ items: Team[] }>('/admin/teams'),

  inviteUser: (input: {
    email: string;
    full_name: string;
    password: string;
    role: string;
    team_id?: string;
  }) => request<User>('/admin/users', { method: 'POST', body: input }),

  createTeam: (name: string, description: string) =>
    request<Team>('/admin/teams', { method: 'POST', body: { name, description } }),
};
