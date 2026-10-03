// Package postgres implements the application's persistence ports.
//
// The single most important property of this package is that no query reaches
// the database without an organisation filter. That is achieved structurally,
// not by discipline:
//
//   - Every port method takes orgID as its first parameter, so a repository
//     method that forgot it would not compile against its interface.
//   - Every SELECT/UPDATE/DELETE below binds organization_id as $1.
//   - A repository test (tenant_isolation_test.go) asserts that entities from
//     one organisation are invisible to another through every read path.
//
// The remaining risk is a *new* query added later that omits the filter, which
// is why the column ordering convention above is uniform: a WHERE clause that
// does not start with organization_id = $1 stands out in review.
package postgres

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// Postgres error codes worth translating rather than leaking as 500s.
const (
	codeUniqueViolation     = "23505"
	codeForeignKeyViolation = "23503"
	codeCheckViolation      = "23514"
	codeInsufficientPrivilege = "42501"
)

// translate converts a driver error into a domain error.
//
// The mapping matters for the API contract: a duplicate email must surface as
// 409 with a code the frontend can act on, not as an opaque 500. Equally, the
// raw driver message is attached as a cause (logged, never serialised) so the
// constraint name is available when debugging without being exposed to callers.
func translate(err error, resource string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return shared.NotFound(resource+".not_found", "not found")
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case codeUniqueViolation:
			return shared.Conflict(resource+".duplicate", "that already exists").
				WithDetail("constraint", pgErr.ConstraintName).
				WithCause(err)
		case codeForeignKeyViolation:
			return shared.Invalid(resource+".reference_invalid", "a referenced record does not exist").
				WithDetail("constraint", pgErr.ConstraintName).
				WithCause(err)
		case codeCheckViolation:
			return shared.Invalid(resource+".constraint_violated", "the value is not permitted").
				WithDetail("constraint", pgErr.ConstraintName).
				WithCause(err)
		case codeInsufficientPrivilege:
			// The audit-log immutability trigger raises this. Reaching it means
			// application code attempted to rewrite history — a bug worth an
			// explicit, unmistakable error rather than a generic failure.
			return shared.Internal(resource+".immutable",
				"this record cannot be modified").WithCause(err)
		}
	}

	return shared.Internal(resource+".query_failed", "a database error occurred").WithCause(err)
}

// notFound builds a consistent not-found error.
func notFound(resource string) error {
	return shared.NotFound(resource+".not_found", "not found")
}

// conflict reports an optimistic-lock failure. The message is written for the
// person who will see it — an agent whose colleague just edited the same
// ticket — not for the developer.
func conflict(resource string) error {
	return shared.Conflict(resource+".stale_write",
		"someone else changed this while you were working on it — reload and try again").
		WithDetail("resource", resource)
}

func wrap(err error, operation string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
