# 0002 — Multi-tenant from day one, single shared schema

**Status:** Accepted

## Context

The guide flags this as the single decision most expensive to retrofit, and it
is right: tenancy touches every table, every query, every endpoint and the whole
auth model. Retrofitting it means auditing all of them at once, on a system that
by then has real data in it.

## Decision

Multi-tenant, with one shared schema and an `organization_id` column on every
tenant-owned table.

Isolation is made structural rather than procedural, in four layers:

1. `identity.Actor` carries `OrgID`, reconstructed from the access token. No
   handler reads an organisation from a request.
2. Every repository read takes `orgID` as its first parameter — enforced by the
   port interfaces, so a method that omitted it would not compile.
3. Every query binds it as `$1`, uniformly, so a `WHERE` clause that does not
   start with `organization_id = $1` is visible in review.
4. `authoriseTicketAccess` asserts the match again on the loaded entity.

## Why not schema-per-tenant

Stronger isolation, and genuinely attractive for compliance. Rejected because
migrations become O(tenants): a schema change has to run against every tenant
schema, and a partial failure leaves the fleet in mixed states. Connection
pooling also degrades, since pools are per-schema.

## Why not database-per-tenant

The strongest isolation and the highest operational cost. Right for a handful of
large enterprise customers; wrong for anything self-service.

## Why not Postgres row-level security

RLS would push enforcement into the database, which is appealing — the guarantee
would hold even for a query that forgot its filter.

Rejected for now because it depends on a session variable being set correctly on
every pooled connection, and a pooler that hands out a connection with a stale
`SET` is a silent cross-tenant leak. That failure mode is worse than the one RLS
prevents, because it is invisible.

## What this costs

- Roughly 15% more code on the persistence layer.
- Correctness rests on a convention plus a test, not on a database guarantee.
- A very large tenant shares indexes with small ones; there is no per-tenant
  tuning.

## Revisit if

An enterprise customer requires physical data separation, or a tenant grows
large enough that its queries degrade others'. RLS specifically becomes worth
revisiting if the connection pool is ever configured session-per-transaction,
which removes the stale-variable risk.
