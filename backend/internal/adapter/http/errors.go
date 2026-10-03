// Package http is the inbound adapter: it translates HTTP into use-case calls
// and domain errors into responses. It contains no business rules — if a
// decision is being made in a handler, it is in the wrong layer.
package http

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// errorBody is the single error shape the API ever returns. One shape means
// the frontend has one error path, and `code` means it can branch on the
// specific failure without parsing prose.
type errorBody struct {
	Error struct {
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		Details   map[string]any `json:"details,omitempty"`
		RequestID string         `json:"request_id,omitempty"`
	} `json:"error"`
}

// statusFor maps a domain error kind to an HTTP status.
//
// The mapping lives here and nowhere else. Handlers that pick their own status
// codes are how an API ends up returning 400 for a permission failure on one
// endpoint and 403 on another.
func statusFor(kind shared.Kind) int {
	switch kind {
	case shared.KindInvalid:
		return http.StatusBadRequest
	case shared.KindUnauthorized:
		return http.StatusUnauthorized
	case shared.KindForbidden:
		return http.StatusForbidden
	case shared.KindNotFound:
		return http.StatusNotFound
	case shared.KindConflict:
		return http.StatusConflict
	case shared.KindRuleViolation:
		// 422: the request was well-formed, but the entity's state refused it.
		// Distinguishing this from 400 is what lets the UI show "you must
		// assign this first" rather than "bad request".
		return http.StatusUnprocessableEntity
	case shared.KindRateLimited:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

// respondError writes the error and, for internal errors, logs the cause.
//
// The cause chain is never serialised. A database constraint name or a driver
// message in a response body tells an attacker about the schema for no benefit
// to a legitimate caller — who cannot act on it either way.
func respondError(c *gin.Context, logger *slog.Logger, err error) {
	kind := shared.KindOf(err)
	status := statusFor(kind)

	var body errorBody
	body.Error.RequestID = requestIDFrom(c)

	if domainErr, ok := shared.AsError(err); ok {
		body.Error.Code = domainErr.Code
		body.Error.Message = domainErr.Message
		body.Error.Details = domainErr.Details
	} else {
		body.Error.Code = "internal_error"
		body.Error.Message = "something went wrong on our end"
	}

	if status >= http.StatusInternalServerError {
		// Log the full chain server-side, including the cause the client will
		// never see, keyed by the request ID the client *will* see — so a user
		// reporting "I got an error" is traceable to the exact log line.
		logger.ErrorContext(c.Request.Context(), "request failed",
			slog.String("request_id", body.Error.RequestID),
			slog.String("path", c.FullPath()),
			slog.String("method", c.Request.Method),
			slog.Any("error", err))
		// Overwrite anything the domain wanted to say: an internal error's
		// message may embed detail that should not cross the boundary.
		body.Error.Code = "internal_error"
		body.Error.Message = "something went wrong on our end"
		body.Error.Details = nil
	}

	c.AbortWithStatusJSON(status, body)
}

// badRequest is a shorthand for request-decoding failures, which happen before
// any use case is reached and so have no domain error to carry.
func badRequest(c *gin.Context, code, message string, details map[string]any) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	body.Error.Details = details
	body.Error.RequestID = requestIDFrom(c)
	c.AbortWithStatusJSON(http.StatusBadRequest, body)
}
