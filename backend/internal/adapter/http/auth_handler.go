package http

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

type AuthHandler struct {
	auth   *app.AuthService
	users  app.UserRepository
	teams  app.TeamRepository
	logger *slog.Logger
}

func NewAuthHandler(auth *app.AuthService, users app.UserRepository,
	teams app.TeamRepository, logger *slog.Logger) *AuthHandler {
	return &AuthHandler{auth: auth, users: users, teams: teams, logger: logger}
}

type registerOrgRequest struct {
	OrgName      string `json:"organization_name" binding:"required,max=120"`
	OrgSlug      string `json:"organization_slug" binding:"required,max=40"`
	TicketPrefix string `json:"ticket_prefix"     binding:"required,max=8"`
	Timezone     string `json:"timezone"          binding:"required,max=64"`
	FullName     string `json:"full_name"         binding:"required,max=120"`
	Email        string `json:"email"             binding:"required,email,max=254"`
	Password     string `json:"password"          binding:"required,min=12,max=128"`
	// Role is deliberately not a field. Self-service signup creates the
	// organisation's first admin and nothing else; every other account is
	// provisioned by that admin. Accepting a role here would be a one-line
	// path to anyone minting themselves an administrator.
}

// RegisterOrganization is the self-service signup for a new tenant.
func (h *AuthHandler) RegisterOrganization(c *gin.Context) {
	var request registerOrgRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, "request.invalid", "please check the details you entered",
			map[string]any{"reason": err.Error()})
		return
	}

	pair, err := h.auth.RegisterOrganization(c.Request.Context(), app.RegisterOrganizationInput{
		OrgName:      request.OrgName,
		OrgSlug:      request.OrgSlug,
		TicketPrefix: request.TicketPrefix,
		Timezone:     request.Timezone,
		AdminEmail:   request.Email,
		AdminName:    request.FullName,
		Password:     request.Password,
	})
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	c.JSON(http.StatusCreated, authResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresAt:    pair.ExpiresAt,
		User:         toUser(pair.User),
	})
}

type loginRequest struct {
	Email    string `json:"email"    binding:"required,email,max=254"`
	Password string `json:"password" binding:"required,max=128"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var request loginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		// A generic message even for a malformed body: distinguishing "your
		// email is not a valid address" from "wrong password" at this endpoint
		// tells an attacker which half of a guess was right.
		respondError(c, h.logger, shared.Unauthorized("auth.invalid_credentials",
			"invalid email or password"))
		return
	}

	pair, err := h.auth.Login(c.Request.Context(), request.Email, request.Password)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	c.JSON(http.StatusOK, authResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresAt:    pair.ExpiresAt,
		User:         toUser(pair.User),
	})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	var request refreshRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, h.logger, shared.Unauthorized("auth.invalid_refresh_token",
			"session expired, please sign in again"))
		return
	}

	pair, err := h.auth.Refresh(c.Request.Context(), request.RefreshToken)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	c.JSON(http.StatusOK, authResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresAt:    pair.ExpiresAt,
		User:         toUser(pair.User),
	})
}

func (h *AuthHandler) Logout(c *gin.Context) {
	var request refreshRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		// Logging out is idempotent and always succeeds from the caller's
		// point of view: the desired end state is "no session", and reporting
		// an error for a missing token would only encourage clients to retry.
		c.Status(http.StatusNoContent)
		return
	}
	if err := h.auth.Logout(c.Request.Context(), request.RefreshToken); err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Me returns the current user, so a page reload can restore session state from
// the access token without the client having to persist a user object.
func (h *AuthHandler) Me(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	user, err := h.users.ByID(c.Request.Context(), actor.OrgID, actor.UserID)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	// Shipping the permission set means the UI hides controls using the exact
	// grants the server enforces, rather than re-deriving them from the role
	// and drifting when the matrix changes.
	permissions := make([]string, 0, 12)
	for permission := range actor.Role.Permissions() {
		permissions = append(permissions, string(permission))
	}

	c.JSON(http.StatusOK, gin.H{
		"user":        toUser(user),
		"permissions": permissions,
	})
}

// RealtimeTicket mints the short-lived credential for the WebSocket handshake.
func (h *AuthHandler) RealtimeTicket(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	realtimeTicket, err := h.auth.IssueRealtimeTicket(c.Request.Context(), actor)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ticket": realtimeTicket})
}

// ---------------------------------------------------------------------------
// User and team administration
// ---------------------------------------------------------------------------

type inviteUserRequest struct {
	Email    string  `json:"email"     binding:"required,email,max=254"`
	FullName string  `json:"full_name" binding:"required,max=120"`
	Password string  `json:"password"  binding:"required,min=12,max=128"`
	Role     string  `json:"role"      binding:"required"`
	TeamID   *string `json:"team_id"`
}

func (h *AuthHandler) InviteUser(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}

	var request inviteUserRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, "request.invalid", "please check the details you entered",
			map[string]any{"reason": err.Error()})
		return
	}

	role, err := identity.ParseRole(request.Role)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	teamID, err := optionalID(request.TeamID)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	user, err := h.auth.InviteUser(c.Request.Context(), actor, app.InviteUserInput{
		Email: request.Email, FullName: request.FullName, Password: request.Password,
		Role: role, TeamID: teamID,
	})
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusCreated, toUser(user))
}

// ListUsers backs the assignee picker and the admin screen.
func (h *AuthHandler) ListUsers(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}

	filter := app.UserFilter{Search: c.Query("q")}
	if raw := c.Query("role"); raw != "" {
		role, err := identity.ParseRole(raw)
		if err != nil {
			respondError(c, h.logger, err)
			return
		}
		filter.Role = &role
	}
	if raw := c.Query("team_id"); raw != "" {
		teamID, err := shared.ParseID(raw)
		if err != nil {
			respondError(c, h.logger, err)
			return
		}
		filter.TeamID = &teamID
	}
	if c.Query("active") == "true" {
		active := true
		filter.Active = &active
	}

	users, err := h.users.List(c.Request.Context(), actor.OrgID, filter)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": toUsers(users)})
}

func (h *AuthHandler) ListTeams(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}
	teams, err := h.teams.List(c.Request.Context(), actor.OrgID)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": toTeams(teams)})
}

type createTeamRequest struct {
	Name        string  `json:"name"        binding:"required,max=80"`
	Description string  `json:"description" binding:"max=500"`
	EscalatesTo *string `json:"escalates_to"`
}

func (h *AuthHandler) CreateTeam(c *gin.Context) {
	actor, ok := mustActor(c, h.logger)
	if !ok {
		return
	}

	var request createTeamRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, "request.invalid", "please check the details you entered",
			map[string]any{"reason": err.Error()})
		return
	}

	team, err := identity.NewTeam(actor.OrgID, request.Name, request.Description, nowUTC())
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	escalatesTo, err := optionalID(request.EscalatesTo)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	if escalatesTo != nil {
		if _, err := h.teams.ByID(c.Request.Context(), actor.OrgID, *escalatesTo); err != nil {
			respondError(c, h.logger, shared.Invalid("team.escalation_unknown",
				"the escalation target team does not exist"))
			return
		}
		team.EscalatesTo = escalatesTo
	}

	if err := h.teams.Create(c.Request.Context(), team); err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusCreated, toTeams([]*identity.Team{team})[0])
}
