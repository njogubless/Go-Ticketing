package shared

// Pagination is keyset ("cursor") based rather than offset based.
//
// Offset pagination on a ticket queue is wrong in two ways: it degrades
// linearly (OFFSET 10000 still walks 10000 rows) and it skips or duplicates
// rows when tickets are created while an agent pages through. Because IDs are
// UUIDv7 and therefore time-ordered, "everything after this ID" is both a
// stable cursor and an index seek.
type Pagination struct {
	// Cursor is the last ID from the previous page. Empty means first page.
	Cursor *ID
	// Limit is clamped by NormalisedLimit; callers may pass anything.
	Limit int
}

const (
	defaultPageLimit = 25
	maxPageLimit     = 100
)

// NormalisedLimit clamps a client-supplied limit into a range the database can
// serve predictably. Trusting a client's `limit=100000` is a denial-of-service
// vector, not a feature.
func (p Pagination) NormalisedLimit() int {
	switch {
	case p.Limit <= 0:
		return defaultPageLimit
	case p.Limit > maxPageLimit:
		return maxPageLimit
	default:
		return p.Limit
	}
}

// Page is a slice of results plus the cursor needed to fetch the next one.
type Page[T any] struct {
	Items      []T
	NextCursor *ID
	HasMore    bool
}

// NewPage builds a Page from an over-fetched slice. Repositories query
// limit+1 rows; if the extra row came back there is another page, and it is
// trimmed off here so the caller never sees it.
func NewPage[T any](items []T, limit int, idOf func(T) ID) Page[T] {
	page := Page[T]{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.HasMore = true
	}
	if len(page.Items) > 0 {
		last := idOf(page.Items[len(page.Items)-1])
		page.NextCursor = &last
	}
	return page
}
