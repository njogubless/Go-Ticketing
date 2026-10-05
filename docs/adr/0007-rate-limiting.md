# 0007 — Per-instance, in-memory rate limiting

**Status:** Accepted, with a known ceiling

## Context

Login is the endpoint attackers actually target, and without a limit credential
stuffing is free.

## Decision

Token buckets held in a map in process memory, with two tiers:

- **Authenticated endpoints** — 300/minute, keyed by user ID.
- **Auth endpoints** — 10/minute, keyed by client IP.

## Why key on user ID where possible

A whole office behind one NAT shares an IP. Keying purely on IP means one busy
user throttles their colleagues, which produces a support ticket about the
ticketing system.

The auth endpoints have no authenticated identity to key on, so they fall back
to IP — and that is also where the tighter limit belongs, since it is the abuse
target.

`SetTrustedProxies(nil)` is set on the router. Gin trusts `X-Forwarded-For` from
every peer by default, which would let any client spoof its IP and defeat the
IP-keyed limit entirely. Behind a load balancer this becomes that balancer's
address, not a blanket trust.

## The ceiling

The buckets are per-instance. Behind N replicas the effective limit is N× the
configured one.

That is an acceptable approximation for abuse control — an attacker still cannot
make unlimited attempts, and 10/minute becoming 30/minute across three instances
does not meaningfully change the economics of credential stuffing. It is a bad
approximation for anything where the exact number matters: quotas, billing, or a
contractual API limit.

## Memory

A bucket per distinct key would grow without bound — one entry per IP, forever,
which is a slow leak that only shows up in production. A janitor goroutine
evicts anything idle for fifteen minutes.

## Revisit if

Any of:

- The limit becomes a quota or a billing input, where N× is wrong rather than
  approximate.
- Replica count grows enough that N× stops being a rounding error.
- A tenant needs a contractual per-organisation API limit.

The upgrade is a Redis-backed sliding window. It is a contained change — the
middleware's interface does not move — but it puts Redis on the request path,
which is a real availability trade and the reason it is not there already.
