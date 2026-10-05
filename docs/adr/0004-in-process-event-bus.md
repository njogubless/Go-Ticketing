# 0004 — In-process event bus

**Status:** Accepted, with a known ceiling

## Context

Ticket changes have side effects — email notifications, realtime pushes to
connected browsers — and the guide is right that coupling those to the ticket
service directly is a mistake. The question was what to decouple them *with*.

## Decision

A buffered channel and a small worker pool, in the same process.
`app.EventPublisher` is the interface; `adapter/eventbus` is the implementation.

## Why not Redis pub/sub or a message queue

At the scale this system starts at, a channel is a message queue with no
operational cost, no additional failure mode, no serialisation and no network
hop. Adding Redis on day one buys nothing and adds a service that can be down.

The `EventPublisher` port is the seam. When one process is no longer enough,
that file is replaced and nothing upstream changes — the ticket service does not
know the hub or the notifier exist, and would not know they had moved.

## What this costs

Two things, both real:

- **Events do not survive a restart.** A ticket resolved as the process receives
  SIGTERM may not send its notification. Mitigated by draining the queue during
  graceful shutdown, but not eliminated.
- **A second replica would not see the first's events.** A user connected to
  instance A gets no realtime update for a ticket changed on instance B. This is
  the hard ceiling: the current design is single-instance for realtime.

## The queue-full policy

`Publish` never blocks. When the buffer (1,024) is full the event is dropped and
logged at error level.

That is the correct failure mode here. The alternative is blocking the HTTP
handler for a ticket change that has *already committed* — turning a
notification backlog into an outage of the thing that actually matters. A
dropped-event log line is the signal to scale consumers or move to a broker.

## Revisit if

Either of these becomes true:

- More than one API replica is needed. Realtime breaks first, silently and
  partially, which is worse than breaking loudly.
- The dropped-event log line appears in normal operation.

The upgrade is Redis pub/sub for fan-out (an hour's work behind the existing
port) or a durable queue if delivery guarantees are needed. Redis is the smaller
step and solves the multi-replica case; only the restart-durability requirement
justifies a real broker.
