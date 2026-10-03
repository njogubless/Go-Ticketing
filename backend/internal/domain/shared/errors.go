// Package shared holds the primitives every other domain package depends on:
// error kinds, identifiers, pagination and the clock abstraction. It imports
// nothing outside the standard library (plus uuid) by design — if this package
// ever needs a framework import, a layering rule has been broken.
package shared

import (
	"errors"
	"fmt"
)

// Kind classifies a domain error so outer layers can map it to a transport
// status without knowing anything about the specific rule that was violated.
// Handlers switch on Kind; they never string-match error messages.
type Kind string

const (
	// KindInvalid means the caller sent something structurally wrong.
	KindInvalid Kind = "invalid"
	// KindNotFound means the entity does not exist, or the caller is not
	// permitted to know that it exists. Repositories return this rather than
	// leaking the difference between "absent" and "belongs to another tenant".
	KindNotFound Kind = "not_found"
	// KindConflict means the request collided with existing state.
	KindConflict Kind = "conflict"
	// KindForbidden means the actor is authenticated but not permitted.
	KindForbidden Kind = "forbidden"
	// KindUnauthorized means the actor is not authenticated.
	KindUnauthorized Kind = "unauthorized"
	// KindRuleViolation means a domain invariant rejected the operation — an
	// illegal state transition, an unmet precondition. Distinct from
	// KindInvalid because the input was well-formed; the *state* said no.
	KindRuleViolation Kind = "rule_violation"
	// KindRateLimited means the caller exceeded an allowance.
	KindRateLimited Kind = "rate_limited"
	// KindInternal means we broke, not the caller.
	KindInternal Kind = "internal"
)

// Error is the single error type crossing domain and application boundaries.
// Code is a stable, machine-readable identifier the frontend can branch on
// (e.g. "ticket.illegal_transition"); Message is safe to show a user; Details
// carries structured context for the client without string parsing.
type Error struct {
	Kind    Kind
	Code    string
	Message string
	Details map[string]any
	cause   error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.cause }

// WithDetail attaches structured context and returns the same error for chaining.
func (e *Error) WithDetail(key string, value any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[key] = value
	return e
}

// WithCause wraps an underlying error. The cause is logged server-side but is
// never serialised to clients — see the HTTP error mapper.
func (e *Error) WithCause(err error) *Error {
	e.cause = err
	return e
}

func newError(kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message}
}

func Invalid(code, message string) *Error       { return newError(KindInvalid, code, message) }
func NotFound(code, message string) *Error      { return newError(KindNotFound, code, message) }
func Conflict(code, message string) *Error      { return newError(KindConflict, code, message) }
func Forbidden(code, message string) *Error     { return newError(KindForbidden, code, message) }
func Unauthorized(code, message string) *Error  { return newError(KindUnauthorized, code, message) }
func RuleViolation(code, msg string) *Error     { return newError(KindRuleViolation, code, msg) }
func RateLimited(code, message string) *Error   { return newError(KindRateLimited, code, message) }
func Internal(code, message string) *Error      { return newError(KindInternal, code, message) }

// KindOf extracts the Kind from any error in the chain, defaulting to
// KindInternal for errors that never passed through the domain. That default
// matters: an unclassified error is a bug, and mapping it to 500 makes it loud.
func KindOf(err error) Kind {
	var domainErr *Error
	if errors.As(err, &domainErr) {
		return domainErr.Kind
	}
	return KindInternal
}

// AsError returns the domain error in the chain, if any.
func AsError(err error) (*Error, bool) {
	var domainErr *Error
	if errors.As(err, &domainErr) {
		return domainErr, true
	}
	return nil, false
}
