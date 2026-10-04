# Web

React 18 + TypeScript + Vite. Two experiences from one app: an agent console and
a requester portal.

## Running

```bash
npm install
npm run dev          # :5173, proxying /api to :8080
```

```bash
npm run typecheck
npm run build
```

`VITE_API_TARGET` overrides the proxy target (default `http://localhost:8080`).

## Layout

```
src/
  api/         wire types + the fetch client (auth, refresh, ETags)
  auth/        session context and the permission helpers
  realtime/    the WebSocket hook
  components/  the shell and shared primitives
  pages/       one file per route
  lib/         formatting
  styles.css   the whole design system
```

## Decisions worth knowing

**The state machine is not duplicated here.** Every ticket read returns
`allowed_transitions`, computed by the server from the rules it enforces, and
the detail page renders one button per entry. A second copy of the transition
table in the client would drift, and the drift would show as buttons that fail.

**Filters live in the URL, not in component state.** That is what makes a queue
shareable — an agent can paste "the P1s breaching today" into chat and a
colleague sees the same list — and it survives a refresh.

**Realtime events invalidate caches; they do not patch them.** Applying an
event's payload straight into the cache would mean reconstructing server state
from a partial diff, and any dropped or reordered event leaves the UI quietly
wrong. Invalidating costs one refetch and cannot desynchronise.

**Permission checks here are cosmetic.** They decide what to *show*. Every one is
enforced again server-side — this file is editable in any browser, so it could
not be a security control even if it wanted to be.

**Access token in memory, refresh token in `localStorage`.** A real trade-off,
not an oversight; the reasoning and the intended fix are in
[ADR 0006](../docs/adr/0006-token-storage.md).

**One in-flight refresh, shared.** Without it a dashboard firing six requests on
an expired token triggers six concurrent refreshes; five present an
already-consumed single-use token, the server correctly reads that as theft, and
the user is logged out by their own dashboard loading.

**TypeScript is fully strict**, including `noUncheckedIndexedAccess` and
`exactOptionalPropertyTypes`. Both caught real bugs during the build — a missing
API field and an `undefined` slipping into a required prop. A half-strict config
gives the syntax tax without the guarantee.

## Design notes

The design system is hand-written CSS rather than a utility framework. A service
desk is one dense table, one dense detail pane and a handful of forms — a narrow
enough surface that semantic class names read better in review than long utility
strings.

Two choices that carry information rather than decoration:

- **Priority colours are not a rainbow.** P1 and P2 are the only ones that
  shout. If every priority is loud, an agent scanning 200 rows cannot find the
  one that matters.
- **A breached row gets a 3px left edge, not a red fill.** Thirty tinted rows are
  unreadable; thirty edges are scannable.

The internal-note composer changes colour with the visibility toggle, so there
is no way to type a note believing it is private when it is not — and vice
versa. That is the one mistake in this UI with a real consequence.

Charts follow the `dataviz` method: form chosen before colour, categorical hues
for unrelated series, a single-hue ordinal ramp for priority (which is ordered,
not categorical), and both palettes run through the validator in light and dark
against this app's own surfaces. The reports route is lazy-loaded — the charting
library is larger than the rest of the application, and agents working the queue
should not download it.
