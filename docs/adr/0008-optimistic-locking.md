# 0008 — Optimistic locking via ETag

**Status:** Accepted

## Context

Two agents open the same ticket. One resolves it with a note; the other, working
from a page loaded five minutes earlier, reassigns it. Under last-write-wins the
second write silently discards the first — including the resolution note.

On a busy desk this is routine, not exotic.

## Decision

Optimistic concurrency control. `TicketRepository.Update` includes
`updated_at = expectedUpdatedAt` in its `WHERE` clause, so a write based on a
stale read affects zero rows and is reported as a 409.

Over HTTP: reads return an `ETag`, writes echo it in `If-Match`.

## Why an ETag rather than Last-Modified

`Last-Modified`/`If-Unmodified-Since` is the more idiomatic pair, and it was the
first implementation. It does not work here: its time format is accurate to the
second, while `updated_at` is stored to the microsecond. The equality check
would essentially never match and every conditional write would fail as a false
conflict.

An opaque ETag carrying `updated_at` at nanosecond precision sidesteps the
format entirely.

## Why not a version column

Functionally equivalent, and slightly cleaner in that it cannot be confused with
a timestamp. `updated_at` was chosen because it already exists, is already
maintained by every write path, and is already sent to the client — a version
column would be a second thing to keep in sync for no additional guarantee.

## Why the check is opt-in

A zero `expectedUpdatedAt` skips the comparison. Making it mandatory would break
every scripted client and every `curl` invocation, for a guarantee only
interactive UIs need — a script that reads and immediately writes is not the
scenario this protects against.

## The trap this hides

The check is only meaningful if the expected value is the one *currently in the
database*. Service methods that mutate the aggregate before calling `Update`
must capture `UpdatedAt` **before** mutating — passing the post-mutation value
compares the new timestamp against the stored old one and never matches.

This was a real bug in four call sites. Every symptom was a spurious 409:
posting a message failed, requesting an approval failed, and both looked like
concurrency problems rather than the self-comparison they were. The capture is
now named `persistedUpdatedAt` at every such site, with a comment, because the
name is what makes the mistake visible in review.

## Error semantics

The repository distinguishes "gone" from "changed" — both match zero rows, but a
404 and a 409 mean different things to the agent looking at the screen. The 409
message says what to do: reload and try again.
