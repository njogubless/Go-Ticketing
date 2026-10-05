# API reference

Base path `/api/v1`. JSON in, JSON out. Authentication is
`Authorization: Bearer <access_token>` on everything except the four public auth
endpoints and the WebSocket upgrade.

## Errors

One shape, everywhere:

```json
{
  "error": {
    "code": "ticket.illegal_transition",
    "message": "that status change is not allowed from the ticket's current status",
    "details": { "from": "new", "to": "resolved", "allowed": ["triaged", "cancelled"] },
    "request_id": "019fd380-5523-7404-be83-3b42b0a7630d"
  }
}
```

Branch on `code`, never on `message`. Codes are a contract; messages are written
for humans and will be reworded. `request_id` appears in the server log line for
the same request, which is what turns "it broke" into something findable.

| Status | Meaning |
|---|---|
| 400 | Malformed request |
| 401 | Not authenticated, or the token is invalid or expired |
| 403 | Authenticated but not permitted |
| 404 | Not found — **or** found but not visible to you (deliberately indistinguishable) |
| 409 | Conflict: duplicate, or a stale write |
| 422 | Well-formed, but the entity's state refused it — an illegal transition, an unmet precondition |
| 429 | Rate limited |

The 404-for-forbidden behaviour is intentional: replying 403 confirms a ticket
exists and turns the ID space into an enumeration oracle.

---

## Auth

| Method | Path | Notes |
|---|---|---|
| POST | `/auth/register-organization` | Creates a tenant and its first admin. No role field — deliberately. |
| POST | `/auth/login` | Returns an access token, a refresh token and the user |
| POST | `/auth/refresh` | Rotates the refresh token. Reuse revokes the whole family. |
| POST | `/auth/logout` | Revokes the presented token's family. Idempotent. |
| GET | `/me` | Current user plus their permission set |
| POST | `/me/realtime-ticket` | A 30-second credential for the WebSocket handshake |

```bash
curl -s localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@acme.test","password":"demo-password-1234"}'
```

---

## Tickets

| Method | Path | Permission |
|---|---|---|
| POST | `/tickets` | `ticket:create` |
| GET | `/tickets` | any — **scope derived from the actor** |
| GET | `/tickets/:id` | any — access-checked |
| GET | `/tickets-by-reference/:ref` | any — access-checked |
| POST | `/tickets/:id/messages` | any — `internal` visibility needs `ticket:note_internal` |
| PATCH | `/tickets/:id/status` | `ticket:transition` |
| PATCH | `/tickets/:id/assignee` | `ticket:assign` |
| PATCH | `/tickets/:id/team` | `ticket:assign` |
| PATCH | `/tickets/:id/classification` | `ticket:transition` |
| PUT/DELETE | `/tickets/:id/assets/:assetId` | `ticket:transition` |
| GET | `/tickets/:id/audit` | `audit:read` |
| POST | `/tickets/:id/approvals` | `ticket:transition` |

### Creating

```json
{
  "kind": "incident",
  "subject": "VPN down for the whole office",
  "description": "Nobody has been able to connect since 09:00.",
  "impact": "high",
  "urgency": "high",
  "category": "network",
  "tags": ["vpn", "outage"],
  "team_id": "…",
  "asset_ids": ["…"],
  "on_behalf_of": "…"
}
```

There is **no `priority` field**. It is derived from impact × urgency — see
[ADR 0005](adr/0005-derived-priority.md). Sending one has no effect.

`on_behalf_of` lets staff raise a ticket for someone who phoned in. Requesters
may only file for themselves.

### Listing

`GET /tickets` accepts:

| Parameter | Example |
|---|---|
| `status` | `new,triaged` |
| `priority` | `P1,P2` |
| `kind` | `incident,change` |
| `team_id` | comma-separated ids |
| `assignee_id` / `requester_id` / `asset_id` | id |
| `unassigned` | `true` |
| `breached` | `true` |
| `tags` | `vpn,outage` |
| `q` | full-text; supports quoted phrases, `OR`, leading `-` |
| `sort` | `newest` \| `oldest` \| `priority` \| `due_soonest` \| `updated` |
| `cursor`, `limit` | keyset pagination, limit clamped to 100 |

The **visibility scope is not a parameter**. It is computed from the actor
server-side, and no query string can widen it.

```json
{ "items": [ … ], "next_cursor": "…", "has_more": true }
```

### Optimistic locking

Reads return an `ETag`. Echo it in `If-Match` on writes to get a 409 instead of
silently overwriting a colleague's change.

```bash
ETAG=$(curl -sD - -o /dev/null localhost:8080/api/v1/tickets/$ID \
  -H "Authorization: Bearer $TOKEN" | grep -i '^etag:' | cut -d' ' -f2 | tr -d '\r')

curl -X PATCH localhost:8080/api/v1/tickets/$ID/status \
  -H "Authorization: Bearer $TOKEN" -H "If-Match: $ETAG" \
  -H 'Content-Type: application/json' \
  -d '{"status":"resolved","resolution":"Restarted the VPN concentrator."}'
```

Omit `If-Match` and the check is skipped — an opt-in, so scripts still work.

### allowed_transitions

Every ticket read includes the statuses reachable *right now*, computed from the
same state machine the server enforces:

```json
{ "status": "in_progress", "allowed_transitions": ["pending_requester", "resolved", "triaged", "cancelled"] }
```

Render buttons from this rather than re-implementing the transition table. A
second copy of the rules will drift, and the drift shows up as buttons that
fail.

---

## Approvals, assets, views, reports, admin

| Method | Path | Permission |
|---|---|---|
| GET | `/approvals/inbox` | `approval:decide` |
| PATCH | `/approvals/:id` | `approval:decide` — a rejection requires a comment |
| GET | `/assets` | `asset:read` |
| POST | `/assets` | `asset:manage` |
| GET | `/assets/hotspots?days=30` | `report:read` |
| GET/POST/DELETE | `/views` | any |
| GET | `/reports/overview?from=&to=&team_id=` | `report:read` |
| GET | `/sla` | `sla:manage` |
| POST | `/sla/policies` | `sla:manage` |
| GET | `/admin/users` | `ticket:read_team` |
| POST | `/admin/users` | `user:manage` |
| GET | `/admin/teams` | any |
| POST | `/admin/teams` | `team:manage` |

The reporting window is clamped to one year. An unbounded aggregate range is a
straightforward way for one authenticated user to saturate the database, and
"last five years" is an export question, not a dashboard one.

---

## Realtime

```
POST /me/realtime-ticket        →  { "ticket": "…" }   (valid 30 seconds)
WS   /api/v1/realtime?ticket=…
```

The access token is deliberately **not** used here. A browser cannot set headers
on a WebSocket upgrade, so something has to travel in the query string — where
every proxy and access log will record it. A 30-second credential scoped to
nothing but a socket subscription is the smaller exposure.

Frames:

```json
{ "type": "ticket.status_changed", "ticket_id": "…", "payload": { … }, "at": "…" }
```

Types: `ticket.created`, `ticket.status_changed`, `ticket.assigned`,
`ticket.routed`, `ticket.message_added`, `ticket.sla_breached`,
`approval.requested`, `approval.decided`.

Fan-out is filtered per connection by the same visibility rules as the REST API.
Internal-only events never reach a requester's socket, whatever their
relationship to the ticket.

The socket is read-only from the client's side. Every mutation goes through
REST, where it is authorised properly — accepting commands here would create a
second authorisation surface, and second surfaces are where the gaps are.

---

## Operational

| Path | Purpose |
|---|---|
| `/healthz` | Liveness. Dependency-free by design: a database blip must not make the orchestrator restart healthy processes. |
| `/readyz` | Readiness. Checks the database, so an instance that cannot serve stops receiving traffic without being killed. |
