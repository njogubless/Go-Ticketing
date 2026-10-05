# 0006 — Short access tokens, rotating refresh tokens

**Status:** Accepted, with a known gap

## Context

A stateless JWT cannot be revoked. Whatever lifetime it is given is the window
during which a stolen token works, and nothing can shorten that window after the
fact.

## Decision

Split the session in two:

- **Access token** — JWT, 15 minutes, stateless, sent as a bearer header. Short
  enough that a leak is survivable.
- **Refresh token** — 32 bytes of randomness, opaque, single-use, rotating,
  stored server-side as a SHA-256 digest.

The refresh token is the revocable half, and it carries the actual session.

## Rotation with reuse detection

Each refresh token belongs to a *family*. Refreshing consumes the presented
token and issues a new one in the same family.

Presenting an already-consumed token means either a race or a stolen token, and
there is no way to tell which — so the entire family is revoked and the user
must sign in again. Losing a session is a small cost against leaving a thief
with a valid rotation chain.

Two details this depends on:

- The revocation is performed **outside** the transaction that detected the
  reuse. Doing it inside would roll it back along with the rejected request —
  the family would survive the very detection meant to kill it. This was a real
  bug, caught by an end-to-end test.
- The client shares **one in-flight refresh** across all callers. Without that, a
  dashboard firing six requests on an expired token triggers six concurrent
  refreshes; five present an already-consumed token, the server correctly reads
  that as theft, and the user is logged out by their own dashboard loading.

## Why SHA-256 rather than bcrypt for the stored digest

The input is 256 bits of uniform randomness. There is no dictionary to attack,
so bcrypt's work factor buys nothing — and the lookup has to stay index-friendly.
Bcrypt here would be cargo-culted cost.

## The known gap

The refresh token is stored in `localStorage`, which is readable by any XSS
payload that ever lands on the page.

This is a deliberate trade, not an oversight: without it, a page reload logs the
user out, which is unusable. The mitigations are that the token is single-use,
rotating, and that reuse revokes the family — so a stolen token is detected the
moment the legitimate client next refreshes.

**The correct answer is an httpOnly, `SameSite=Strict`, `Secure` cookie for the
refresh token**, which XSS cannot read. It requires CSRF protection on the
refresh endpoint and same-site deployment of the API and the app. It is the
change to make before a public launch.

The access token is held **in memory only** and never persisted — a tab close
ends it.

## Revisit

Before any public deployment. This is the highest-priority item in the backlog.
