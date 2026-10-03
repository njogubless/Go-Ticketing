package shared

import "time"

// Clock exists so that SLA arithmetic, token expiry and "is this breached yet"
// checks are testable without sleeping. Every piece of code that needs the
// current time takes a Clock; nothing in domain/ or app/ calls time.Now()
// directly.
type Clock interface {
	Now() time.Time
}

// SystemClock is the production implementation. It returns UTC — the whole
// system stores and reasons in UTC and converts to a business calendar's
// timezone only inside the SLA calculator.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// FixedClock is a test double. Advance() moves it forward deterministically.
type FixedClock struct{ Current time.Time }

func NewFixedClock(t time.Time) *FixedClock { return &FixedClock{Current: t.UTC()} }

func (c *FixedClock) Now() time.Time { return c.Current }

func (c *FixedClock) Advance(d time.Duration) { c.Current = c.Current.Add(d) }
