package http

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
)

type TicketHandler struct {
	tickets *app.TicketService
	views   *app.ViewService
	clock   shared.Clock
	logger  *slog.Logger
}

func NewTicketHandler(tickets *app.TicketService, views *app.ViewService,
	clock shared.Clock, logger *slog.Logger) *TicketHandler {
	return &TicketHandler{tickets: tickets, views: views, clock: clock, logger: logger}
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

type createTicketRequest struct {
	Kind        string   `json:"kind"        binding:"required"`
	Subject     string   `json:"subject"     binding:"required,max=200"`
	Description string   `json:"description" binding:"required,max=20000"`
	Category    string   `json:"category"    binding:"max=60"`
	Tags        []string `json:"tags"        binding:"max=12,dive,max=40"`
	Impact      string   `json:"impact"      binding:"required"`
	Urgency     string   `json:"urgency"     binding:"required"`
	TeamID      *string  `json:"team_id"`
	AssetIDs    []string `json:"asset_ids"   binding:"max=20"`
	OnBehalfOf  *string  `json:"on_behalf_of"`
	// Priority is deliberately absent. It is derived from impact and urgency;
	// accepting it would let a client bypass the matrix that keeps priority
	// comparable across the whole desk.
}

func (h *TicketHandler) Create(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}

	var request createTicketRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, "request.invalid", "the request body could not be read", map[string]any{
			"reason": err.Error(),
		})
		return
	}

	// Enum parsing happens at the boundary so the rest of the system works
	// with types that are valid by construction.
	kind, err := ticket.ParseKind(request.Kind)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	impact, err := ticket.ParseImpact(request.Impact)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	urgency, err := ticket.ParseUrgency(request.Urgency)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	teamID, err := optionalID(request.TeamID)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	onBehalfOf, err := optionalID(request.OnBehalfOf)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	assetIDs, err := parseIDs(request.AssetIDs)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	view, err := h.tickets.Create(c.Request.Context(), actor, app.CreateTicketInput{
		Kind: kind, Subject: request.Subject, Description: request.Description,
		Category: request.Category, Tags: request.Tags,
		Impact: impact, Urgency: urgency, TeamID: teamID,
		AssetIDs: assetIDs, OnBehalfOf: onBehalfOf,
	})
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	c.JSON(http.StatusCreated, toTicketDetail(view, h.clock.Now()))
}

// ---------------------------------------------------------------------------
// Read
// ---------------------------------------------------------------------------

func (h *TicketHandler) Get(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	ticketID, err := shared.ParseID(c.Param("id"))
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	view, err := h.tickets.Get(c.Request.Context(), actor, ticketID)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	setETag(c, view.Ticket.UpdatedAt)
	c.JSON(http.StatusOK, toTicketDetail(view, h.clock.Now()))
}

func (h *TicketHandler) GetByReference(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	view, err := h.tickets.GetByReference(c.Request.Context(), actor, c.Param("reference"))
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, toTicketDetail(view, h.clock.Now()))
}

// List runs the ticket search.
func (h *TicketHandler) List(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}

	query, err := parseTicketQuery(c)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	page, err := h.tickets.Search(c.Request.Context(), actor, query)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	now := h.clock.Now()
	c.JSON(http.StatusOK, toPage(page, func(t *ticket.Ticket) ticketSummary {
		return toTicketSummary(t, now)
	}))
}

// parseTicketQuery reads filters from the query string.
//
// Note what it does *not* read: anything to do with visibility. The scope is
// set from the actor inside the service, and a query parameter cannot widen
// it. This is why the filter type and the scope type are separate.
func parseTicketQuery(c *gin.Context) (app.TicketQuery, error) {
	query := app.TicketQuery{}

	for _, raw := range splitCSV(c.Query("status")) {
		status, err := ticket.ParseStatus(raw)
		if err != nil {
			return query, err
		}
		query.Statuses = append(query.Statuses, status)
	}
	for _, raw := range splitCSV(c.Query("priority")) {
		priority, err := ticket.ParsePriority(raw)
		if err != nil {
			return query, err
		}
		query.Priorities = append(query.Priorities, priority)
	}
	for _, raw := range splitCSV(c.Query("kind")) {
		kind, err := ticket.ParseKind(raw)
		if err != nil {
			return query, err
		}
		query.Kinds = append(query.Kinds, kind)
	}
	for _, raw := range splitCSV(c.Query("team_id")) {
		teamID, err := shared.ParseID(raw)
		if err != nil {
			return query, err
		}
		query.TeamIDs = append(query.TeamIDs, teamID)
	}

	if tags := splitCSV(c.Query("tags")); len(tags) > 0 {
		query.Tags = tags
	}

	if c.Query("unassigned") == "true" {
		query.Unassigned = true
	} else if raw := c.Query("assignee_id"); raw != "" {
		assigneeID, err := shared.ParseID(raw)
		if err != nil {
			return query, err
		}
		query.AssigneeID = &assigneeID
	}

	if raw := c.Query("requester_id"); raw != "" {
		requesterID, err := shared.ParseID(raw)
		if err != nil {
			return query, err
		}
		query.RequesterID = &requesterID
	}
	if raw := c.Query("asset_id"); raw != "" {
		assetID, err := shared.ParseID(raw)
		if err != nil {
			return query, err
		}
		query.AssetID = &assetID
	}

	if text := strings.TrimSpace(c.Query("q")); text != "" {
		if len(text) > 200 {
			return query, shared.Invalid("search.query_too_long", "search text is too long")
		}
		query.Text = text
	}

	query.BreachedOnly = c.Query("breached") == "true"

	if raw := c.Query("due_before"); raw != "" {
		dueBefore, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return query, shared.Invalid("search.due_before_invalid", "due_before must be an RFC3339 timestamp")
		}
		query.DueBefore = &dueBefore
	}
	if raw := c.Query("created_after"); raw != "" {
		createdAfter, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return query, shared.Invalid("search.created_after_invalid", "created_after must be an RFC3339 timestamp")
		}
		query.CreatedAfter = &createdAfter
	}

	// Sort is matched against a closed set. An unrecognised value falls back
	// to the default rather than erroring — a stale bookmark should still
	// load the queue.
	switch app.TicketSort(c.Query("sort")) {
	case app.SortOldest:
		query.Sort = app.SortOldest
	case app.SortPriority:
		query.Sort = app.SortPriority
	case app.SortDueSoonest:
		query.Sort = app.SortDueSoonest
	case app.SortUpdated:
		query.Sort = app.SortUpdated
	default:
		query.Sort = app.SortNewest
	}

	if raw := c.Query("cursor"); raw != "" {
		cursor, err := shared.ParseID(raw)
		if err != nil {
			return query, shared.Invalid("search.cursor_invalid", "the pagination cursor is not valid")
		}
		query.Page.Cursor = &cursor
	}
	if raw := c.Query("limit"); raw != "" {
		if limit, err := strconv.Atoi(raw); err == nil {
			query.Page.Limit = limit // clamped by NormalisedLimit
		}
	}

	return query, nil
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

type transitionRequest struct {
	Status     string  `json:"status"     binding:"required"`
	Resolution *string `json:"resolution" binding:"omitempty,max=10000"`
}

func (h *TicketHandler) Transition(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	ticketID, err := shared.ParseID(c.Param("id"))
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	var request transitionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, "request.invalid", "the request body could not be read",
			map[string]any{"reason": err.Error()})
		return
	}
	status, err := ticket.ParseStatus(request.Status)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	view, err := h.tickets.Transition(c.Request.Context(), actor, ticketID, app.TransitionInput{
		To:                status,
		Resolution:        request.Resolution,
		ExpectedUpdatedAt: ifMatch(c),
	})
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	setETag(c, view.Ticket.UpdatedAt)
	c.JSON(http.StatusOK, toTicketDetail(view, h.clock.Now()))
}

type assignRequest struct {
	// A pointer distinguishes "assign to nobody" (explicit null) from "field
	// omitted". Only the former is meaningful here, and conflating them would
	// make unassigning impossible.
	AssigneeID *string `json:"assignee_id"`
}

func (h *TicketHandler) Assign(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	ticketID, err := shared.ParseID(c.Param("id"))
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	var request assignRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, "request.invalid", "the request body could not be read",
			map[string]any{"reason": err.Error()})
		return
	}
	assigneeID, err := optionalID(request.AssigneeID)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	view, err := h.tickets.Assign(c.Request.Context(), actor, ticketID, app.AssignInput{
		AssigneeID:        assigneeID,
		ExpectedUpdatedAt: ifMatch(c),
	})
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, toTicketDetail(view, h.clock.Now()))
}

type routeRequest struct {
	TeamID *string `json:"team_id"`
}

func (h *TicketHandler) Route(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	ticketID, err := shared.ParseID(c.Param("id"))
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	var request routeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, "request.invalid", "the request body could not be read",
			map[string]any{"reason": err.Error()})
		return
	}
	teamID, err := optionalID(request.TeamID)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	view, err := h.tickets.Route(c.Request.Context(), actor, ticketID, app.RouteInput{
		TeamID:            teamID,
		ExpectedUpdatedAt: ifMatch(c),
	})
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, toTicketDetail(view, h.clock.Now()))
}

type reclassifyRequest struct {
	Impact  string `json:"impact"  binding:"required"`
	Urgency string `json:"urgency" binding:"required"`
}

func (h *TicketHandler) Reclassify(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	ticketID, err := shared.ParseID(c.Param("id"))
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	var request reclassifyRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, "request.invalid", "the request body could not be read",
			map[string]any{"reason": err.Error()})
		return
	}
	impact, err := ticket.ParseImpact(request.Impact)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	urgency, err := ticket.ParseUrgency(request.Urgency)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	view, err := h.tickets.Reclassify(c.Request.Context(), actor, ticketID, app.ReclassifyInput{
		Impact: impact, Urgency: urgency, ExpectedUpdatedAt: ifMatch(c),
	})
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, toTicketDetail(view, h.clock.Now()))
}

type addMessageRequest struct {
	Body string `json:"body" binding:"required,max=50000"`
	// Visibility defaults to public when omitted. Defaulting the *other* way
	// would be safer against leaks but would make every requester reply
	// invisible to the requester, so the safe default here is the visible one
	// — and posting an internal note requires an explicit permission anyway.
	Visibility string `json:"visibility"`
}

func (h *TicketHandler) AddMessage(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	ticketID, err := shared.ParseID(c.Param("id"))
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	var request addMessageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, "request.invalid", "the request body could not be read",
			map[string]any{"reason": err.Error()})
		return
	}

	visibility := ticket.VisibilityPublic
	if request.Visibility != "" {
		visibility, err = ticket.ParseVisibility(request.Visibility)
		if err != nil {
			respondError(c, h.logger, err)
			return
		}
	}

	message, err := h.tickets.AddMessage(c.Request.Context(), actor, ticketID, app.AddMessageInput{
		Body: request.Body, Visibility: visibility,
	})
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	c.JSON(http.StatusCreated, messageResponse{
		ID: message.ID, AuthorID: message.AuthorID, Body: message.Body,
		Visibility: message.Visibility, System: message.System, CreatedAt: message.CreatedAt,
	})
}

func (h *TicketHandler) LinkAsset(c *gin.Context) {
	h.assetLink(c, true)
}

func (h *TicketHandler) UnlinkAsset(c *gin.Context) {
	h.assetLink(c, false)
}

func (h *TicketHandler) assetLink(c *gin.Context, link bool) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	ticketID, err := shared.ParseID(c.Param("id"))
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	assetID, err := shared.ParseID(c.Param("assetId"))
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	if link {
		err = h.tickets.LinkAsset(c.Request.Context(), actor, ticketID, assetID)
	} else {
		err = h.tickets.UnlinkAsset(c.Request.Context(), actor, ticketID, assetID)
	}
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// AuditTrail returns a ticket's history.
func (h *TicketHandler) AuditTrail(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	ticketID, err := shared.ParseID(c.Param("id"))
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	page := shared.Pagination{}
	if raw := c.Query("cursor"); raw != "" {
		cursor, err := shared.ParseID(raw)
		if err != nil {
			respondError(c, h.logger, err)
			return
		}
		page.Cursor = &cursor
	}
	if raw := c.Query("limit"); raw != "" {
		if limit, err := strconv.Atoi(raw); err == nil {
			page.Limit = limit
		}
	}

	entries, err := h.tickets.AuditTrail(c.Request.Context(), actor, ticketID, page)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	type auditEntryResponse struct {
		ID         shared.ID `json:"id"`
		ActorLabel string    `json:"actor_label"`
		Action     string    `json:"action"`
		From       *string   `json:"from,omitempty"`
		To         *string   `json:"to,omitempty"`
		CreatedAt  time.Time `json:"created_at"`
	}

	c.JSON(http.StatusOK, toPage(entries, func(entry *ticket.AuditEntry) auditEntryResponse {
		return auditEntryResponse{
			ID: entry.ID, ActorLabel: entry.ActorLabel, Action: string(entry.Action),
			From: entry.From, To: entry.To, CreatedAt: entry.CreatedAt,
		}
	}))
}

// ---------------------------------------------------------------------------
// Shared parsing helpers
// ---------------------------------------------------------------------------

func optionalID(raw *string) (*shared.ID, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	id, err := shared.ParseID(*raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func parseIDs(raws []string) ([]shared.ID, error) {
	ids := make([]shared.ID, 0, len(raws))
	for _, raw := range raws {
		id, err := shared.ParseID(raw)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// The optimistic-lock token is an ETag carrying the ticket's updated_at at
// nanosecond precision.
//
// Last-Modified / If-Unmodified-Since would be the more idiomatic HTTP pair,
// but its time format is only accurate to the second while updated_at is
// stored to the microsecond — so the equality check would essentially never
// match and every conditional write would fail as a false conflict. An opaque
// ETag sidesteps the format entirely.

func setETag(c *gin.Context, updatedAt time.Time) {
	c.Header("ETag", strconv.Quote(updatedAt.UTC().Format(time.RFC3339Nano)))
}

// ifMatch reads the token back. A zero time means the client did not send one,
// which the repository treats as "no concurrency check" — a deliberate opt-in
// rather than a hard requirement, so scripted clients and curl still work.
func ifMatch(c *gin.Context) time.Time {
	raw := strings.TrimSpace(c.GetHeader("If-Match"))
	if raw == "" {
		return time.Time{}
	}
	if unquoted, err := strconv.Unquote(raw); err == nil {
		raw = unquoted
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}
