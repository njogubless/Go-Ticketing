/**
 * Wire types, mirroring the Go DTOs in internal/adapter/http/dto.go.
 *
 * These are hand-written rather than generated. Generation from an OpenAPI
 * spec would be the right call once the API stabilises; while it is still
 * moving, a hand-written mirror is one file to update and it forces a moment's
 * thought about whether a backend change is really a contract change.
 *
 * The enums are string unions rather than TypeScript `enum`s: they compare
 * directly against JSON values with no runtime object, and exhaustive
 * `switch`es over them are checked by the compiler.
 */

export type Role = 'requester' | 'agent' | 'manager' | 'admin';

export type TicketKind = 'incident' | 'service_request' | 'change' | 'problem';

export type TicketStatus =
  | 'new'
  | 'triaged'
  | 'pending_approval'
  | 'in_progress'
  | 'pending_requester'
  | 'resolved'
  | 'closed'
  | 'cancelled';

export type Priority = 'P1' | 'P2' | 'P3' | 'P4';
export type Impact = 'low' | 'medium' | 'high';
export type Urgency = 'low' | 'medium' | 'high';
export type Visibility = 'public' | 'internal';
export type AssetKind =
  | 'laptop' | 'desktop' | 'mobile' | 'server' | 'network'
  | 'printer' | 'software' | 'service' | 'license' | 'other';
export type AssetStatus = 'in_stock' | 'assigned' | 'maintenance' | 'retired';
export type Criticality = 'low' | 'medium' | 'high';
export type ApprovalDecision = 'pending' | 'approved' | 'rejected';

export interface User {
  id: string;
  email: string;
  full_name: string;
  role: Role;
  team_id?: string;
  active: boolean;
}

export interface Team {
  id: string;
  name: string;
  description: string;
  escalates_to?: string;
}

export interface AuthResponse {
  access_token: string;
  refresh_token: string;
  expires_at: string;
  user: User;
}

export interface MeResponse {
  user: User;
  permissions: string[];
}

export interface TicketSummary {
  id: string;
  reference: string;
  kind: TicketKind;
  subject: string;
  status: TicketStatus;
  priority: Priority;
  impact: Impact;
  urgency: Urgency;
  category: string;
  tags: string[];
  requester_id: string;
  assignee_id?: string;
  team_id?: string;
  resolution_due?: string;
  resolved_at?: string;
  closed_at?: string;
  /** Computed server-side — client clocks disagree often enough to matter. */
  breached: boolean;
  reopen_count: number;
  created_at: string;
  updated_at: string;
}

export interface Message {
  id: string;
  author_id?: string;
  body: string;
  visibility: Visibility;
  system: boolean;
  created_at: string;
}

export interface Asset {
  id: string;
  tag: string;
  name: string;
  kind: AssetKind;
  status: AssetStatus;
  criticality: Criticality;
  model?: string;
  location?: string;
  owner_id?: string;
}

export interface Approval {
  id: string;
  approver_id: string;
  requested_by: string;
  decision: ApprovalDecision;
  comment?: string;
  decided_at?: string;
  created_at: string;
}

export interface TicketDetail extends TicketSummary {
  description: string;
  resolution?: string;
  requester?: User;
  assignee?: User;
  messages: Message[];
  assets: Asset[];
  approvals: Approval[];
  first_response_due?: string;
  first_response_at?: string;
  paused_since?: string;
  /**
   * Comes from the server's own state machine. The UI renders exactly these
   * as buttons rather than re-implementing the transition table — one source
   * of truth, so the two cannot drift.
   */
  allowed_transitions: TicketStatus[];
  /** False for requesters, so the thread can be labelled honestly. */
  can_read_internal: boolean;
}

export interface Page<T> {
  items: T[];
  next_cursor?: string;
  has_more: boolean;
}

export interface AuditEntry {
  id: string;
  actor_label: string;
  action: string;
  from?: string;
  to?: string;
  created_at: string;
}

export interface Stats {
  created: number;
  resolved: number;
  reopened: number;
  open: number;
  breached: number;
  first_response_attainment_pct: number;
  resolution_attainment_pct: number;
  median_resolution_seconds: number;
  p90_resolution_seconds: number;
  by_status: Record<string, number>;
  by_priority: Record<string, number>;
  by_kind: Record<string, number>;
  volume: Array<{ day: string; created: number; resolved: number }>;
  agent_load: Array<{ user_id: string; full_name: string; open: number; breached: number }>;
}

export interface SavedView {
  id: string;
  name: string;
  shared: boolean;
  system: boolean;
  filter: Record<string, unknown>;
}

export interface SLAPolicy {
  id: string;
  name: string;
  priority?: Priority;
  kind?: TicketKind;
  team_id?: string;
  calendar_id: string;
  first_response_seconds: number;
  resolution_seconds: number;
  active: boolean;
}

export interface SLACalendar {
  id: string;
  name: string;
  timezone: string;
  windows: Array<{ weekday: number; start_mins: number; end_mins: number }>;
}

/** The single error shape the API returns, from internal/adapter/http/errors.go. */
export interface ApiErrorBody {
  error: {
    code: string;
    message: string;
    details?: Record<string, unknown>;
    request_id?: string;
  };
}

/** A realtime event as delivered over the WebSocket. */
export interface RealtimeEvent {
  type: string;
  ticket_id: string;
  payload?: Record<string, unknown>;
  at: string;
}

export interface TicketFilters {
  status?: TicketStatus[];
  priority?: Priority[];
  kind?: TicketKind[];
  assignee_id?: string;
  requester_id?: string;
  team_id?: string[];
  unassigned?: boolean;
  breached?: boolean;
  tags?: string[];
  q?: string;
  sort?: 'newest' | 'oldest' | 'priority' | 'due_soonest' | 'updated';
  asset_id?: string;
}
