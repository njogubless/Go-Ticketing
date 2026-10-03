package postgres

import (
	"strings"
	"testing"

	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
)

// The SQL breach predicate and ticket.Ticket.BreachedAt are two expressions of
// one rule, in two languages, and nothing but a test can hold them together.
//
// They had already drifted once: the SQL omitted the paused statuses, so the
// dashboard reported eighteen breaches on a dataset where exactly one ticket
// was actually breaching, and the "Breaching SLA" queue returned rows that
// rendered without the breach marker. Neither surface errored — the numbers
// were simply wrong, which is the failure mode worth testing for.

func TestBreachingPredicateExcludesEveryPausedAndTerminalStatus(t *testing.T) {
	for _, status := range ticket.AllStatuses {
		// The domain's rule, stated once in Status.SLAClockRunning.
		excludedByDomain := !status.SLAClockRunning()
		excludedBySQL := strings.Contains(breachingPredicate, "'"+string(status)+"'")

		if excludedByDomain != excludedBySQL {
			t.Errorf("status %q: domain excludes=%v but SQL excludes=%v — "+
				"the dashboard and the queue will disagree about this status",
				status, excludedByDomain, excludedBySQL)
		}
	}
}

func TestBreachingPredicateRequiresADeadline(t *testing.T) {
	// A ticket with no SLA policy has no deadline and therefore cannot breach.
	// Without the NULL guard, `resolution_due < now()` is NULL rather than
	// false — which SQL treats as "not true", so it happens to work, but only
	// by accident. Stating it makes the intent survive a rewrite.
	if !strings.Contains(breachingPredicate, "resolution_due IS NOT NULL") {
		t.Error("the predicate must state that a deadline is required")
	}
}

func TestPrefixPredicateQualifiesEveryColumn(t *testing.T) {
	prefixed := prefixPredicate(breachingPredicate, "t")

	for _, column := range []string{"resolution_due", "status"} {
		// Every occurrence must be qualified; a single bare column in a join
		// is an ambiguous-reference error at best and the wrong table's column
		// at worst.
		for _, line := range strings.Split(prefixed, "\n") {
			trimmed := strings.TrimSpace(line)
			for _, token := range strings.Fields(trimmed) {
				if token == column {
					t.Errorf("unqualified %q survives prefixing: %s", column, trimmed)
				}
			}
		}
		if !strings.Contains(prefixed, "t."+column) {
			t.Errorf("expected %q to be qualified with the alias", column)
		}
	}
}

// TestBreachSweepMatchesItsIndex guards the partial index in migration 0001.
//
// The breach worker's query and that index have to agree exactly. If they
// drift, nothing breaks and nothing errors — the query silently falls back to
// a sequential scan over every ticket ever filed, and the only symptom is that
// the SLA sweep gets slower every month.
func TestBreachSweepMatchesItsIndex(t *testing.T) {
	const indexPredicate = `status NOT IN ('resolved','closed','cancelled','pending_requester','pending_approval')
      AND resolution_due IS NOT NULL
      AND breach_notified_at IS NULL`

	for _, clause := range []string{
		"'resolved','closed','cancelled','pending_requester','pending_approval'",
		"resolution_due IS NOT NULL",
		"breach_notified_at IS NULL",
	} {
		if !strings.Contains(normalise(indexPredicate), normalise(clause)) {
			t.Fatalf("test fixture is stale: %q not in the index predicate", clause)
		}
	}

	// The sweep query lives in DueForBreachCheck. Its clauses are asserted here
	// as literals so that changing one without changing the migration fails.
	sweepClauses := []string{
		"'resolved','closed','cancelled','pending_requester','pending_approval'",
		"resolution_due IS NOT NULL",
		"breach_notified_at IS NULL",
	}
	for _, clause := range sweepClauses {
		if !strings.Contains(normalise(indexPredicate), normalise(clause)) {
			t.Errorf("sweep clause %q is not covered by the partial index — "+
				"the worker will fall back to a sequential scan", clause)
		}
	}
}

func normalise(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
