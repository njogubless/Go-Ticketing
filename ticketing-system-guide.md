# Building a Ticketing System — A Complete Engineering Guide

> A reference document to study bit by bit. Covers concept, architecture, tech stack, timeline, cost, and what to focus on as the engineer building it.

---

## 1. What a Ticketing System Actually Does

At its core, a ticketing system converts **unstructured requests** (a complaint, a bug, a support question, an incident) into **structured, trackable units of work** ("tickets") that move through a defined lifecycle until resolved.

Every ticketing system — whether it's Zendesk, Jira, Freshdesk, or a custom internal tool — is solving the same fundamental problem:

> **"Nothing should fall through the cracks, and everyone should know who owns what, and by when."**

### Core things it does:
- **Intake** — captures a request from multiple channels (email, web form, chat, API, phone-to-text)
- **Classification** — categorizes/tags/prioritizes the request (bug, billing, feature request, P1 incident, etc.)
- **Routing/Assignment** — sends it to the right person, team, or queue (manually or via rules/AI)
- **Tracking** — maintains status (Open → In Progress → Pending → Resolved → Closed) and full history
- **Communication** — threads all replies (internal notes vs customer-facing replies) in one place
- **SLA enforcement** — tracks response/resolution time commitments and escalates breaches
- **Reporting** — gives visibility into volume, bottlenecks, agent performance, recurring issues
- **Knowledge capture** — often feeds a knowledge base / FAQ from resolved tickets

---

## 2. What It Entails (Scope Breakdown)

A "ticketing system" is really several sub-systems working together:

| Subsystem | Responsibility |
|---|---|
| **Ticket Core** | CRUD for tickets, status/priority state machine, custom fields |
| **User & Identity** | Customers, agents, teams, roles, permissions (RBAC) |
| **Communication Layer** | Threaded messages, internal notes, email/SMS ingestion & replies |
| **Notification Engine** | Email, push, in-app alerts on ticket events |
| **SLA/Automation Engine** | Rule-based triggers, escalations, auto-assignment |
| **Search & Filtering** | Full-text search, saved views, queues |
| **Reporting/Analytics** | Dashboards, exportable reports |
| **Integrations** | Email providers, Slack, CRM, webhooks, third-party APIs |
| **Admin/Config** | Custom statuses, forms, workflows, business hours |
| **Audit Trail** | Who changed what and when (compliance-critical in many industries) |

**How "versed" (broad) a project this is:** It's a **medium-to-large** system depending on depth. A minimal MVP (create/view/assign/close tickets) is a weekend-to-2-week build. A production-grade system with SLA automation, omnichannel intake, RBAC, reporting, and integrations is a **3–9 month** effort for a small team, longer solo. It's comparable in complexity to building a lightweight CRM.

---

## 3. Why It's Needful for a Company / The Problem It Solves

Without a ticketing system, companies default to email inboxes, spreadsheets, or Slack threads for support/issue tracking. This breaks down because:

- **Requests get lost** — no single source of truth, things live in someone's inbox
- **No accountability** — unclear who owns a request or its deadline
- **No visibility** — leadership can't see volume, trends, or bottlenecks
- **Poor customer experience** — customers repeat themselves, get inconsistent answers
- **No historical context** — new agents can't see prior interactions with a customer
- **Compliance risk** — no audit trail for regulated industries (finance, healthcare)
- **Doesn't scale** — email/Slack works for 5 requests/week, collapses at 500/week

A ticketing system solves this by giving the company **structure, accountability, and measurability** over what would otherwise be chaos.

---

## 4. Best Approach to Building It

### Recommended approach: **Domain-first, not feature-first**

1. **Model the domain first** — Ticket, Status, Priority, Agent, Team, Customer, Comment/Message, Attachment, SLA Policy, Tag/Category. Get this right before writing any UI.
2. **Build the state machine explicitly** — ticket status transitions should be an enforced state machine, not a free-text field. (e.g., you shouldn't be able to jump from "Open" to "Closed" without "Resolved" if your business rules require it.)
3. **Start with a monolith** — Don't over-engineer with microservices on day one. A well-structured modular monolith (e.g., Django apps, or a layered FastAPI service) is the right starting point. Split into services only when a specific part (e.g., notifications, or email ingestion) genuinely needs independent scaling.
4. **Build the core loop before automation** — Create → Assign → Comment → Resolve → Close must work rock-solid before you touch SLA automation, AI routing, or omnichannel intake.
5. **Treat notifications and email ingestion as their own mini-projects** — these are deceptively complex (inbound email parsing, deliverability, threading by Message-ID, retries) and often underestimated.
6. **Design for multi-tenancy from day one if this will ever serve multiple companies** — retrofitting tenancy later is painful. If it's for a single internal company, skip this.

---

## 5. System Design

### 5.1 High-Level Architecture

```
┌─────────────┐      ┌──────────────────┐      ┌─────────────────┐
│  Clients    │◄────►│   API Gateway /   │◄────►│   Core Services  │
│ Web / Mobile│      │   Backend (DRF/   │      │  - Ticket Svc    │
│ Agent Portal│      │   FastAPI)        │      │  - Auth/RBAC     │
└─────────────┘      └──────────────────┘      │  - Notification  │
                              │                  │  - SLA Engine    │
                              ▼                  └─────────────────┘
                      ┌──────────────┐                   │
                      │  PostgreSQL   │◄──────────────────┘
                      └──────────────┘
                              │
                ┌─────────────┼──────────────┐
                ▼             ▼              ▼
          ┌──────────┐ ┌────────────┐ ┌──────────────┐
          │  Redis    │ │ Celery /   │ │ Object Store │
          │ (cache,   │ │ background │ │ (attachments)│
          │  queues)  │ │  workers)  │ │  S3/Cloudinary│
          └──────────┘ └────────────┘ └──────────────┘
                              │
                      ┌──────────────┐
                      │ Email/SMTP,  │
                      │ Webhooks,    │
                      │ Slack, etc.  │
                      └──────────────┘
```

### 5.2 Core Data Model (simplified)

- **User** (id, role: customer/agent/admin, team_id)
- **Team** (id, name)
- **Ticket** (id, subject, description, status, priority, requester_id, assignee_id, team_id, category, created_at, sla_due_at)
- **Message/Comment** (id, ticket_id, author_id, body, is_internal_note, created_at)
- **Attachment** (id, message_id, file_url)
- **Tag** (id, name) + TicketTag (m2m)
- **SLAPolicy** (id, priority, first_response_target, resolution_target)
- **AuditLog** (id, ticket_id, actor_id, action, old_value, new_value, timestamp)
- **Notification** (id, user_id, ticket_id, channel, sent_at)

### 5.3 Key Design Decisions to Make Deliberately

1. **Status as enum + explicit transition rules**, not free text
2. **Soft state machine enforcement** — either in application code (a `TicketStateMachine` class) or DB constraints
3. **Event-driven side effects** — ticket status change → emit event → notification worker listens (decouples core logic from side effects; use Celery/RQ + Redis, or a simple signal/event bus)
4. **Idempotent email ingestion** — inbound emails must be deduplicated by Message-ID to avoid duplicate tickets
5. **Row-level permissions** — agents should only see tickets for their team unless admin; enforce this at the query layer, not just UI
6. **Full-text search** — Postgres `tsvector`/GIN index is enough at small-medium scale; Elasticsearch/Meilisearch only once volume justifies it
7. **Audit everything** — status changes, assignment changes, and internal note visibility should be logged

---

## 6. Best Frameworks, Languages & Tools

Given your stack (Django/DRF, FastAPI, React/TypeScript, Flutter), here's what fits well — no need to learn a new ecosystem:

### Backend
- **Django + DRF** — strong default choice. Django's admin panel alone saves weeks for internal agent tooling; DRF gives you serializers, permissions, viewsets out of the box; Django's ORM handles the relational complexity (tickets, teams, SLAs) cleanly.
- **FastAPI** — better if you want async-heavy performance (e.g., high-throughput webhook/email ingestion, or if you want to pair it with WebSockets for real-time ticket updates). You'd hand-roll more (auth, admin, ORM via SQLAlchemy).
- **Recommendation**: Django/DRF for the core system (fast to build, batteries included), and consider a **FastAPI microservice only for the notification/email-ingestion worker** if you want async I/O there.

### Frontend
- **React + TypeScript** — right choice. Use it for both the **customer portal** and the **agent dashboard**. Consider separating them as two apps/routes since the UX and permissions differ heavily.
- State management: React Query/TanStack Query for server state (tickets, comments) pairs very well with a ticketing UI's real-time-ish needs.

### Mobile
- **Flutter** — useful for an agent mobile app (quick triage on the go) or a lightweight customer support app. Not essential for MVP.

### Infrastructure/Tooling
- **PostgreSQL** — relational integrity matters here (tickets ↔ users ↔ SLAs)
- **Redis** — caching + Celery broker
- **Celery** (with Django) or **Arq/Dramatiq** (with FastAPI) — background jobs: sending emails, SLA breach checks, digest notifications
- **Django Channels** or **FastAPI WebSockets** — for real-time ticket updates (optional but valuable UX)
- **S3-compatible storage** (AWS S3, Cloudinary, or Backblaze B2) — attachments
- **SendGrid/Postmark/Mailgun** — outbound email + inbound email parsing (most have "parse webhook" features that save you from building raw SMTP ingestion)
- **Docker** — for consistent dev/deploy environments
- **Sentry** — error tracking (important in a system where "a ticket got lost" is the worst-case bug)

---

## 7. What to Know & Focus On as the Engineer

### Focus areas, ranked by importance:
1. **State machines** — this is the conceptual heart of the system. Get comfortable with explicit state transition modeling (even a simple dict-based transition map).
2. **Permissions/RBAC design** — who can see/do what is the most bug-prone area (customers seeing other customers' tickets is a serious data leak).
3. **Idempotency & deduplication** — especially for email/webhook ingestion.
4. **Background job design** — SLA checks, notifications, and escalations all run outside the request/response cycle. Get comfortable with Celery/task queues.
5. **Database indexing & query performance** — ticket lists get filtered/sorted constantly (by status, assignee, team, date); this is where naive queries slow down first.
6. **Audit logging patterns** — you'll want a reusable pattern (e.g., Django signals or a decorator) rather than manually logging in every view.
7. **API design for a UI-heavy product** — pagination, filtering, and search on the ticket-list endpoint will be hit constantly; design it well early.

### What you should prepare/study before starting:
- Finite state machines (conceptually — you don't need a library, just the mental model)
- RBAC patterns (role-based vs. permission-based access control)
- Email protocols basics (SMTP for sending, inbound parse webhooks for receiving) — you don't need deep protocol knowledge, just how the providers abstract it
- Event-driven/observer patterns (for decoupling "ticket updated" from "send notification")
- Basic understanding of SLA/escalation logic (business time vs. calendar time — e.g., SLAs often exclude weekends/after-hours, which is a surprisingly fiddly calculation)

---

## 8. Timeline Expectations

Assuming solo development, part-time/portfolio pace (your typical build-in-public cadence):

| Phase | Scope | Estimated Time |
|---|---|---|
| **Planning & data modeling** | Domain model, ERD, wireframes | 3–5 days |
| **MVP core** | Auth, ticket CRUD, status/assignment, basic UI | 2–3 weeks |
| **Communication layer** | Comments/threads, internal notes, attachments | 1–2 weeks |
| **Notifications** | Email on ticket events, in-app notifications | 1 week |
| **SLA engine** | Policies, due-date calculation, breach alerts | 1–2 weeks |
| **Search, filters, saved views** | Full-text search, queue views | 3–5 days |
| **Reporting dashboard** | Basic charts (volume, resolution time, agent load) | 1 week |
| **Polish, testing, deployment** | E2E tests, CI/CD, hosting | 1–2 weeks |

**Total for a solid portfolio-grade MVP with SLA + reporting: ~2–3 months part-time.**
A bare-bones "create/assign/close tickets" MVP alone: **2–3 weeks**.

---

## 9. Cost Estimate

### If building solo as a portfolio project:
| Item | Monthly Cost |
|---|---|
| Hosting (Railway/Render/Fly.io small instance) | $5–20 |
| Managed Postgres (or self-hosted on same box) | $0–15 |
| Redis (managed, small) | $0–10 |
| Object storage (S3/Backblaze, low volume) | $0–5 |
| Transactional email (SendGrid/Postmark free tier) | $0–15 |
| Domain name | ~$1/month amortized |
| Error tracking (Sentry free tier) | $0 |
| **Total** | **$10–60/month** — very affordable at portfolio scale |

### If building this as a commercial product/for a company:
- **Solo freelance build (custom, mid-complexity)**: $8,000–$25,000, depending on scope
- **Small agency/team build (production-grade, SLA, integrations, multi-tenant)**: $30,000–$100,000+
- **Ongoing infra at real company scale** (thousands of tickets/month, multiple agents): $200–$2,000+/month depending on email volume, storage, and uptime requirements
- **Buy vs. build**: This is the honest alternative most companies choose — Zendesk/Freshdesk/Help Scout start at $15–50/agent/month. Companies build custom only when: they need deep integration with proprietary systems, have unusual compliance needs, or ticketing volume/cost makes a SaaS fee unjustifiable at scale.

---

## 10. Benefits — From All Perspectives

**For the company:**
- Reduced response/resolution time → higher customer satisfaction
- Clear accountability and ownership of issues
- Data-driven insight into recurring problems (product/ops feedback loop)
- Scalable support operations without proportional headcount growth
- Audit trail for compliance-sensitive industries

**For support agents:**
- One place to work from instead of scattered inboxes
- Context on every customer interaction history
- Clear queues/priorities instead of guessing what to do next

**For customers:**
- Faster, more consistent responses
- Transparency into where their issue stands
- No need to repeat themselves to different agents

**For you as the engineer (portfolio value):**
- Demonstrates full-stack depth: auth/RBAC, state machines, async workers, real-time features, third-party integrations, reporting
- A ticketing system is a **recognizable, relatable problem** to any technical interviewer — easy to explain design decisions and trade-offs
- Forces you to practice patterns that transfer directly to fintech/marketplace work you already do (workflow states, notifications, audit trails) — strong complement to your existing [[hapopay]] and [[changa-backend]] work

---

## 11. Prerequisites Before Starting

- [ ] Comfortable with your chosen backend framework's auth system (Django's auth + DRF permissions, or FastAPI + OAuth2/JWT)
- [ ] Understand relational database design well enough to model a moderately complex schema (6–10 related tables)
- [ ] Basic experience with a background task queue (Celery, RQ, Arq, or similar)
- [ ] Comfortable setting up a transactional email provider and reading their webhook docs
- [ ] A rough wireframe/mockup of the two main UIs: agent dashboard and customer portal (even a rough Figma or hand sketch helps avoid rework)
- [ ] Decide scope up front: internal tool vs. multi-tenant SaaS — this single decision affects your data model, auth, and billing considerations from day one

---

## 12. Suggested Build Order (Practical Checklist)

1. Define domain model + ERD
2. Set up project skeleton (Django/DRF or FastAPI) + auth
3. Build Ticket CRUD + status state machine
4. Build assignment logic (manual first, rules-based later)
5. Build comment/thread system (internal vs. external notes)
6. Add attachments (object storage)
7. Add email notifications on key events
8. Build the frontend: ticket list, ticket detail, create form
9. Add search/filtering/saved views
10. Add SLA policies + due-date calculation + breach alerts
11. Add basic reporting dashboard
12. Write tests for the state machine and permission boundaries (highest-risk areas)
13. Deploy, monitor with Sentry, iterate

---

*Document generated for reference — read through section by section rather than all at once. Sections 5–7 (system design, stack, focus areas) are the most technically dense; sections 8–10 (timeline, cost, benefits) are lighter and good for planning conversations.*
