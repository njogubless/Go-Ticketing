// Package eventbus provides in-process publish/subscribe.
//
// Deliberately in-process rather than Redis or a message queue. At the scale
// this system starts at, a channel is a message queue with no operational cost,
// no extra failure mode and no serialisation. The EventPublisher interface in
// the app layer is the seam: when one process is no longer enough, this file is
// replaced by a Redis or NATS adapter and nothing upstream changes.
//
// The trade-off is explicit and worth stating: events do not survive a restart,
// and a multi-replica deployment needs the external bus. Both are fine until
// they are not, and the ADR in docs/ records when to revisit.
package eventbus

import (
	"context"
	"log/slog"
	"sync"

	"github.com/blessnduta/ticketing-system/internal/app"
)

// Handler consumes an event. Handlers run on the bus's own goroutine pool, not
// the caller's, so a slow handler cannot make an agent's request slow.
type Handler func(ctx context.Context, event app.Event)

// Bus fans events out to registered handlers.
type Bus struct {
	mu       sync.RWMutex
	handlers map[app.EventType][]Handler
	// wildcard handlers receive every event — used by the audit-ish consumers
	// (the WebSocket hub) that care about all ticket activity.
	wildcard []Handler

	queue  chan app.Event
	logger *slog.Logger
	wg     sync.WaitGroup
	closed chan struct{}
	once   sync.Once
}

// New creates a bus with a buffered queue and a fixed worker pool.
//
// Buffer size is the interesting number. Too small and a burst blocks the
// request path; too large and a stuck consumer hides behind a growing backlog
// until memory runs out. 1024 absorbs a realistic burst — a bulk import, a
// breach sweep escalating fifty tickets — while still being small enough that
// a genuinely stuck consumer surfaces within seconds.
func New(logger *slog.Logger, workers int) *Bus {
	if workers <= 0 {
		workers = 4
	}
	bus := &Bus{
		handlers: make(map[app.EventType][]Handler),
		queue:    make(chan app.Event, 1024),
		logger:   logger,
		closed:   make(chan struct{}),
	}
	for i := 0; i < workers; i++ {
		bus.wg.Add(1)
		go bus.worker()
	}
	return bus
}

var _ app.EventPublisher = (*Bus)(nil)

// Subscribe registers a handler for one event type.
func (b *Bus) Subscribe(eventType app.EventType, handler Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventType] = append(b.handlers[eventType], handler)
}

// SubscribeAll registers a handler for every event type.
func (b *Bus) SubscribeAll(handler Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.wildcard = append(b.wildcard, handler)
}

// Publish enqueues an event. It never blocks.
//
// When the queue is full the event is dropped and logged at error level. That
// is the correct failure mode here: the alternative is blocking the HTTP
// handler that already committed the ticket change, turning a notification
// backlog into an outage of the thing that actually matters. The dropped-event
// log line is the signal to scale the consumers or move to an external broker.
func (b *Bus) Publish(ctx context.Context, event app.Event) {
	select {
	case <-b.closed:
		return
	default:
	}

	select {
	case b.queue <- event:
	default:
		b.logger.ErrorContext(ctx, "event bus queue full, dropping event",
			slog.String("event_type", string(event.Type)),
			slog.String("ticket_id", event.TicketID.String()))
	}
}

func (b *Bus) worker() {
	defer b.wg.Done()
	for event := range b.queue {
		b.dispatch(event)
	}
}

func (b *Bus) dispatch(event app.Event) {
	b.mu.RLock()
	handlers := append([]Handler(nil), b.handlers[event.Type]...)
	handlers = append(handlers, b.wildcard...)
	b.mu.RUnlock()

	for _, handler := range handlers {
		// Each handler is isolated: a panic in the notifier must not take down
		// the worker goroutine and with it every subsequent event.
		func(handler Handler) {
			defer func() {
				if recovered := recover(); recovered != nil {
					b.logger.Error("event handler panicked",
						slog.String("event_type", string(event.Type)),
						slog.Any("panic", recovered))
				}
			}()
			// Handlers get a background context: the request that produced the
			// event has already returned, and inheriting its (now-cancelled)
			// context would cancel every notification.
			handler(context.Background(), event)
		}(handler)
	}
}

// Close drains the queue and stops the workers. Called during graceful
// shutdown so in-flight notifications are delivered rather than lost.
func (b *Bus) Close() {
	b.once.Do(func() {
		close(b.closed)
		close(b.queue)
		b.wg.Wait()
	})
}
