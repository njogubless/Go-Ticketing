# 0005 — Priority is derived from impact × urgency

**Status:** Accepted

## Context

Most ticketing systems let the person raising a ticket pick a priority.

Within a quarter, everything is High. The people who most need help are not the
people most willing to claim urgency, and once the distribution collapses the
queue ordering stops carrying information — which removes the main reason to
have a priority field at all.

## Decision

Priority is computed, never accepted. Two inputs:

- **Impact** — how much of the organisation is affected (one person / a team /
  everyone).
- **Urgency** — how fast it degrades (a workaround exists / degraded / work is
  stopped).

|  | Urgency high | Urgency medium | Urgency low |
|---|---|---|---|
| **Impact high** | P1 | P2 | P3 |
| **Impact medium** | P2 | P3 | P4 |
| **Impact low** | P3 | P4 | P4 |

The API does not have a priority field on create or update. Sending one is
ignored — there is nothing to ignore it, because the DTO has no such field.
Changing priority means reclassifying impact or urgency, which is audited.

## Why this works better

The two inputs are questions the requester can answer honestly, because neither
is "how important are you?". "Can you still work?" has a true answer.

It also makes priority comparable across teams. A P2 from Finance and a P2 from
Engineering mean the same thing, because both went through the same matrix — so
a shared queue can be sorted by it.

The frontend shows the resulting priority before the requester submits, which is
the gentlest available discouragement from selecting the most severe option: the
consequence is visible at the point of choosing.

## What this costs

- Requesters occasionally disagree with the computed priority. The desk can
  reclassify, and the reclassification is recorded — which is more accountable
  than someone silently editing a free-choice field.
- The matrix is a business rule embedded in code. It is written out in full,
  rather than computed from a score, so that it can be read and amended
  directly.

## Revisit if

An organisation needs per-tenant matrices. The table is a package-level variable
today; making it per-organisation configuration is a contained change.
