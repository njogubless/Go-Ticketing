package http

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/blessnduta/ticketing-system/internal/adapter/realtime"
	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/approval"
	"github.com/blessnduta/ticketing-system/internal/domain/asset"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
)

func nowUTC() time.Time { return time.Now().UTC() }

// ---------------------------------------------------------------------------
// Assets
// ---------------------------------------------------------------------------

type AssetHandler struct {
	assets *app.AssetService
	logger *slog.Logger
}

func NewAssetHandler(assets *app.AssetService, logger *slog.Logger) *AssetHandler {
	return &AssetHandler{assets: assets, logger: logger}
}

type createAssetRequest struct {
	Tag           string     `json:"tag"          binding:"required,max=60"`
	Name          string     `json:"name"         binding:"required,max=160"`
	Kind          string     `json:"kind"         binding:"required"`
	Criticality   string     `json:"criticality"  binding:"required"`
	Manufacturer  string     `json:"manufacturer" binding:"max=120"`
	Model         string     `json:"model"        binding:"max=120"`
	SerialNumber  string     `json:"serial_number" binding:"max=120"`
	Location      string     `json:"location"     binding:"max=120"`
	OwnerID       *string    `json:"owner_id"`
	ParentID      *string    `json:"parent_id"`
	PurchasedAt   *time.Time `json:"purchased_at"`
	WarrantyUntil *time.Time `json:"warranty_until"`
}

func (h *AssetHandler) Create(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}

	var request createAssetRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, "request.invalid", "please check the details you entered",
			map[string]any{"reason": err.Error()})
		return
	}

	kind, err := asset.ParseKind(request.Kind)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	criticality, err := asset.ParseCriticality(request.Criticality)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	ownerID, err := optionalID(request.OwnerID)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	parentID, err := optionalID(request.ParentID)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	created, err := h.assets.Create(c.Request.Context(), actor, app.CreateAssetInput{
		Tag: request.Tag, Name: request.Name, Kind: kind, Criticality: criticality,
		Manufacturer: request.Manufacturer, Model: request.Model,
		SerialNumber: request.SerialNumber, Location: request.Location,
		OwnerID: ownerID, ParentID: parentID,
		PurchasedAt: request.PurchasedAt, WarrantyUntil: request.WarrantyUntil,
	})
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusCreated, toAsset(created))
}

func (h *AssetHandler) List(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}

	filter := app.AssetFilter{Search: c.Query("q")}
	if raw := c.Query("kind"); raw != "" {
		kind, err := asset.ParseKind(raw)
		if err != nil {
			respondError(c, h.logger, err)
			return
		}
		filter.Kind = &kind
	}
	if raw := c.Query("status"); raw != "" {
		status, err := asset.ParseStatus(raw)
		if err != nil {
			respondError(c, h.logger, err)
			return
		}
		filter.Status = &status
	}
	if raw := c.Query("owner_id"); raw != "" {
		ownerID, err := shared.ParseID(raw)
		if err != nil {
			respondError(c, h.logger, err)
			return
		}
		filter.OwnerID = &ownerID
	}
	if raw := c.Query("cursor"); raw != "" {
		cursor, err := shared.ParseID(raw)
		if err != nil {
			respondError(c, h.logger, err)
			return
		}
		filter.Page.Cursor = &cursor
	}
	if raw := c.Query("limit"); raw != "" {
		if limit, err := strconv.Atoi(raw); err == nil {
			filter.Page.Limit = limit
		}
	}

	page, err := h.assets.List(c.Request.Context(), actor, filter)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, toPage(page, toAsset))
}

// Hotspots ranks the assets generating the most incidents.
func (h *AssetHandler) Hotspots(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}

	days := 30
	if raw := c.Query("days"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}
	limit := 10
	if raw := c.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	counts, err := h.assets.Hotspots(c.Request.Context(), actor,
		nowUTC().AddDate(0, 0, -days), limit)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	type hotspot struct {
		Asset assetResponse `json:"asset"`
		Count int           `json:"incident_count"`
	}
	items := make([]hotspot, 0, len(counts))
	for _, entry := range counts {
		items = append(items, hotspot{Asset: toAsset(entry.Asset), Count: entry.Count})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "window_days": days})
}

// ---------------------------------------------------------------------------
// Approvals
// ---------------------------------------------------------------------------

type ApprovalHandler struct {
	approvals *app.ApprovalService
	logger    *slog.Logger
}

func NewApprovalHandler(approvals *app.ApprovalService, logger *slog.Logger) *ApprovalHandler {
	return &ApprovalHandler{approvals: approvals, logger: logger}
}

type requestApprovalRequest struct {
	ApproverIDs []string `json:"approver_ids" binding:"required,min=1,max=10"`
}

func (h *ApprovalHandler) Request(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	ticketID, err := shared.ParseID(c.Param("id"))
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	var request requestApprovalRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, "request.invalid", "please check the details you entered",
			map[string]any{"reason": err.Error()})
		return
	}
	approverIDs, err := parseIDs(request.ApproverIDs)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	created, err := h.approvals.Request(c.Request.Context(), actor, ticketID, approverIDs)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"items": toApprovals(created)})
}

type decideRequest struct {
	Decision string `json:"decision" binding:"required,oneof=approved rejected"`
	Comment  string `json:"comment"  binding:"max=2000"`
}

func (h *ApprovalHandler) Decide(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	approvalID, err := shared.ParseID(c.Param("id"))
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	var request decideRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, "request.invalid", "please check the details you entered",
			map[string]any{"reason": err.Error()})
		return
	}

	decided, err := h.approvals.Decide(c.Request.Context(), actor, approvalID, app.DecideInput{
		Decision: approval.Decision(request.Decision), Comment: request.Comment,
	})
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, toApprovals([]*approval.Approval{decided})[0])
}

// Inbox lists approvals waiting on the caller.
func (h *ApprovalHandler) Inbox(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	pending, err := h.approvals.Inbox(c.Request.Context(), actor)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": toApprovals(pending)})
}

func toApprovals(approvals []*approval.Approval) []approvalResponse {
	out := make([]approvalResponse, 0, len(approvals))
	for _, record := range approvals {
		out = append(out, approvalResponse{
			ID: record.ID, ApproverID: record.ApproverID, RequestedBy: record.RequestedBy,
			Decision: record.Decision, Comment: record.Comment,
			DecidedAt: record.DecidedAt, CreatedAt: record.CreatedAt,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Reporting and SLA
// ---------------------------------------------------------------------------

type ReportHandler struct {
	reports *app.ReportService
	sla     *app.SLAService
	logger  *slog.Logger
}

func NewReportHandler(reports *app.ReportService, slaService *app.SLAService, logger *slog.Logger) *ReportHandler {
	return &ReportHandler{reports: reports, sla: slaService, logger: logger}
}

func (h *ReportHandler) Overview(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}

	var from, to *time.Time
	if raw := c.Query("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondError(c, h.logger, shared.Invalid("report.from_invalid",
				"from must be an RFC3339 timestamp"))
			return
		}
		from = &parsed
	}
	if raw := c.Query("to"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondError(c, h.logger, shared.Invalid("report.to_invalid",
				"to must be an RFC3339 timestamp"))
			return
		}
		to = &parsed
	}

	var teamID *shared.ID
	if raw := c.Query("team_id"); raw != "" {
		parsed, err := shared.ParseID(raw)
		if err != nil {
			respondError(c, h.logger, err)
			return
		}
		teamID = &parsed
	}

	stats, err := h.reports.Overview(c.Request.Context(), actor, from, to, teamID)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, toStats(stats))
}

func (h *ReportHandler) ListSLA(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	snapshot, err := h.sla.List(c.Request.Context(), actor)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, toSLASnapshot(snapshot))
}

type createPolicyRequest struct {
	Name                 string  `json:"name"                   binding:"required,max=120"`
	Priority             *string `json:"priority"`
	Kind                 *string `json:"kind"`
	TeamID               *string `json:"team_id"`
	CalendarID           string  `json:"calendar_id"            binding:"required"`
	FirstResponseSeconds int64   `json:"first_response_seconds" binding:"required,min=60"`
	ResolutionSeconds    int64   `json:"resolution_seconds"     binding:"required,min=60"`
	EscalateAfterSeconds *int64  `json:"escalate_after_seconds"`
}

func (h *ReportHandler) CreatePolicy(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}

	var request createPolicyRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, "request.invalid", "please check the details you entered",
			map[string]any{"reason": err.Error()})
		return
	}

	calendarID, err := shared.ParseID(request.CalendarID)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	teamID, err := optionalID(request.TeamID)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	input := app.CreatePolicyInput{
		Name:          request.Name,
		TeamID:        teamID,
		CalendarID:    calendarID,
		FirstResponse: time.Duration(request.FirstResponseSeconds) * time.Second,
		Resolution:    time.Duration(request.ResolutionSeconds) * time.Second,
	}
	if request.Priority != nil {
		priority, err := ticket.ParsePriority(*request.Priority)
		if err != nil {
			respondError(c, h.logger, err)
			return
		}
		input.Priority = &priority
	}
	if request.Kind != nil {
		kind, err := ticket.ParseKind(*request.Kind)
		if err != nil {
			respondError(c, h.logger, err)
			return
		}
		input.Kind = &kind
	}
	if request.EscalateAfterSeconds != nil {
		escalateAfter := time.Duration(*request.EscalateAfterSeconds) * time.Second
		input.EscalateAfter = &escalateAfter
	}

	policy, err := h.sla.CreatePolicy(c.Request.Context(), actor, input)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": policy.ID, "name": policy.Name})
}

// ---------------------------------------------------------------------------
// Saved views
// ---------------------------------------------------------------------------

type ViewHandler struct {
	views   *app.ViewService
	tickets *app.TicketService
	clock   shared.Clock
	logger  *slog.Logger
}

func NewViewHandler(views *app.ViewService, tickets *app.TicketService,
	clock shared.Clock, logger *slog.Logger) *ViewHandler {
	return &ViewHandler{views: views, tickets: tickets, clock: clock, logger: logger}
}

type savedViewResponse struct {
	ID     shared.ID `json:"id"`
	Name   string    `json:"name"`
	Shared bool      `json:"shared"`
	// System marks the built-in views, which the UI renders above the
	// user-created ones and does not offer a delete control for.
	System bool           `json:"system"`
	Filter map[string]any `json:"filter"`
}

func (h *ViewHandler) List(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}

	stored, err := h.views.List(c.Request.Context(), actor)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	items := make([]savedViewResponse, 0, len(stored)+4)
	for _, view := range app.SystemViews(actor) {
		items = append(items, savedViewResponse{
			ID: view.ID, Name: view.Name, System: true, Filter: filterToMap(view.Filter),
		})
	}
	for _, view := range stored {
		items = append(items, savedViewResponse{
			ID: view.ID, Name: view.Name, Shared: view.Shared, Filter: filterToMap(view.Filter),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type createViewRequest struct {
	Name   string `json:"name"   binding:"required,max=60"`
	Shared bool   `json:"shared"`
}

// Create saves the caller's current filter set as a named view. The filter is
// read from the query string — the same parser the list endpoint uses — so a
// view always means exactly what the agent was looking at.
func (h *ViewHandler) Create(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}

	var request createViewRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, "request.invalid", "please check the details you entered",
			map[string]any{"reason": err.Error()})
		return
	}

	filter, err := parseTicketQuery(c)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	view, err := h.views.Create(c.Request.Context(), actor, request.Name, request.Shared, filter)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusCreated, savedViewResponse{
		ID: view.ID, Name: view.Name, Shared: view.Shared, Filter: filterToMap(view.Filter),
	})
}

func (h *ViewHandler) Delete(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	viewID, err := shared.ParseID(c.Param("id"))
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	if err := h.views.Delete(c.Request.Context(), actor, viewID); err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// filterToMap projects a stored filter into the query-string shape the client
// already knows how to build a URL from, so a saved view is just a link.
func filterToMap(query app.TicketQuery) map[string]any {
	filter := map[string]any{}
	if len(query.Statuses) > 0 {
		filter["status"] = query.Statuses
	}
	if len(query.Priorities) > 0 {
		filter["priority"] = query.Priorities
	}
	if len(query.Kinds) > 0 {
		filter["kind"] = query.Kinds
	}
	if query.Unassigned {
		filter["unassigned"] = true
	}
	if query.AssigneeID != nil {
		filter["assignee_id"] = query.AssigneeID
	}
	if query.BreachedOnly {
		filter["breached"] = true
	}
	if query.Text != "" {
		filter["q"] = query.Text
	}
	if query.Sort != "" {
		filter["sort"] = query.Sort
	}
	return filter
}

// ---------------------------------------------------------------------------
// Realtime
// ---------------------------------------------------------------------------

type RealtimeHandler struct {
	hub            *realtime.Hub
	issuer         app.TokenIssuer
	allowedOrigins map[string]bool
	logger         *slog.Logger
}

func NewRealtimeHandler(hub *realtime.Hub, issuer app.TokenIssuer,
	allowedOrigins []string, logger *slog.Logger) *RealtimeHandler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = true
	}
	return &RealtimeHandler{hub: hub, issuer: issuer, allowedOrigins: allowed, logger: logger}
}

// Connect upgrades to a WebSocket after validating a short-lived ticket.
//
// Origin checking is mandatory here and cannot be delegated to CORS: the
// same-origin policy does not apply to WebSockets, so a browser will happily
// open one from any site the user is visiting, carrying their cookies. Without
// this check that is cross-site WebSocket hijacking.
func (h *RealtimeHandler) Connect(c *gin.Context) {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			// A missing Origin means a non-browser client (a CLI, a test). It
			// has no ambient credentials to be abused, and it still has to
			// present a valid ticket below.
			if origin == "" {
				return true
			}
			return h.allowedOrigins[origin]
		},
	}

	realtimeTicket := c.Query("ticket")
	if realtimeTicket == "" {
		respondError(c, h.logger, shared.Unauthorized("realtime.ticket_required",
			"a realtime ticket is required"))
		return
	}

	actor, err := h.issuer.ParseRealtimeTicket(realtimeTicket)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrade has already written its own error response.
		h.logger.WarnContext(c.Request.Context(), "websocket upgrade failed", slog.Any("error", err))
		return
	}

	h.hub.Attach(conn, actor)
}
