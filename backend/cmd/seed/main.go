// Command seed populates a development database with a realistic service desk.
//
// "Realistic" is doing real work here. A seed of three tickets called "test 1",
// "test 2", "test 3" proves the API responds; it does not show whether the
// queue is usable at 200 tickets, whether the priority sort is sensible, or
// whether the SLA panel looks right when half the queue is breaching. This
// generates enough shaped data to answer those questions.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"time"

	"github.com/blessnduta/ticketing-system/internal/adapter/postgres"
	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/approval"
	"github.com/blessnduta/ticketing-system/internal/domain/asset"
	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
	"github.com/blessnduta/ticketing-system/internal/platform/config"
	"github.com/blessnduta/ticketing-system/internal/platform/database"
	"github.com/blessnduta/ticketing-system/internal/platform/logging"
	"github.com/blessnduta/ticketing-system/internal/platform/security"
)

func main() {
	tickets := flag.Int("tickets", 120, "how many tickets to generate")
	slug := flag.String("slug", "acme", "organisation slug")
	flag.Parse()

	if err := run(*slug, *tickets); err != nil {
		fmt.Fprintf(os.Stderr, "seed failed: %v\n", err)
		os.Exit(1)
	}
}

const demoPassword = "demo-password-1234"

func run(slug string, ticketCount int) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// Seeding a production database would be a genuine incident: fake tickets
	// in a real audit trail cannot be cleanly removed.
	if cfg.IsProduction() {
		return fmt.Errorf("refusing to seed a production environment")
	}

	logger := logging.New("info", "text")
	ctx := context.Background()

	pool, err := database.Connect(ctx, database.Config{
		URL: cfg.DatabaseURL, MaxConns: 10, MinConns: 1, MaxConnLifetime: time.Hour,
	}, logger)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pool.Migrate(ctx); err != nil {
		return err
	}

	clock := shared.SystemClock{}
	// Cost 4 rather than 12: seeding 30 users at production cost takes about
	// ten seconds of pure hashing for accounts nobody will attack.
	hasher := security.NewBcryptHasher(4)
	issuer := security.NewJWTIssuer(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RealtimeTicketTTL)

	orgRepo := postgres.NewOrganizationRepo(pool)
	userRepo := postgres.NewUserRepo(pool)
	teamRepo := postgres.NewTeamRepo(pool)
	ticketRepo := postgres.NewTicketRepo(pool)
	messageRepo := postgres.NewMessageRepo(pool)
	auditRepo := postgres.NewAuditRepo(pool)
	slaRepo := postgres.NewSLARepo(pool)
	assetRepo := postgres.NewAssetRepo(pool)
	approvalRepo := postgres.NewApprovalRepo(pool)
	tokenRepo := postgres.NewRefreshTokenRepo(pool)

	slaService := app.NewSLAService(slaRepo, ticketRepo, userRepo, teamRepo, auditRepo, nil, clock, logger)
	authService := app.NewAuthService(pool, orgRepo, userRepo, teamRepo, slaRepo, tokenRepo,
		hasher, issuer, clock, cfg.RefreshTokenTTL)
	ticketService := app.NewTicketService(pool, ticketRepo, messageRepo, auditRepo, userRepo,
		teamRepo, orgRepo, assetRepo, approvalRepo, slaService, nil, clock)
	assetService := app.NewAssetService(assetRepo, userRepo, clock)
	approvalService := app.NewApprovalService(pool, approvalRepo, ticketRepo, userRepo,
		auditRepo, nil, clock)

	if existing, err := orgRepo.BySlug(ctx, slug); err == nil && existing != nil {
		return fmt.Errorf("organisation %q already exists — drop the database or pick another --slug", slug)
	}

	logger.Info("seeding", slog.String("slug", slug), slog.Int("tickets", ticketCount))

	// --- organisation and admin ---------------------------------------------
	pair, err := authService.RegisterOrganization(ctx, app.RegisterOrganizationInput{
		OrgName:      "Acme Corporation",
		OrgSlug:      slug,
		TicketPrefix: "ACME",
		Timezone:     "Africa/Nairobi",
		AdminEmail:   fmt.Sprintf("admin@%s.test", slug),
		AdminName:    "Ada Admin",
		Password:     demoPassword,
	})
	if err != nil {
		return fmt.Errorf("creating organisation: %w", err)
	}
	admin := pair.User.Actor()

	// --- teams ---------------------------------------------------------------
	teams := map[string]shared.ID{}
	for _, name := range []string{"Network & Infrastructure", "End User Support", "Applications", "Security"} {
		team, err := identity.NewTeam(admin.OrgID, name, "", clock.Now())
		if err != nil {
			return err
		}
		if err := teamRepo.Create(ctx, team); err != nil {
			return err
		}
		teams[name] = team.ID
	}

	// Escalation chain: front-line support escalates to infrastructure, which
	// escalates to security. The breach worker walks this.
	if support, err := teamRepo.ByID(ctx, admin.OrgID, teams["End User Support"]); err == nil {
		network := teams["Network & Infrastructure"]
		support.EscalatesTo = &network
		_ = teamRepo.Update(ctx, support)
	}

	// --- people ---------------------------------------------------------------
	type person struct {
		name string
		role identity.Role
		team string
	}
	roster := []person{
		{"Wanjiru Manager", identity.RoleManager, "Network & Infrastructure"},
		{"Amina Otieno", identity.RoleAgent, "Network & Infrastructure"},
		{"Brian Kimani", identity.RoleAgent, "Network & Infrastructure"},
		{"Chege Mwangi", identity.RoleAgent, "End User Support"},
		{"Diana Achieng", identity.RoleAgent, "End User Support"},
		{"Esther Njeri", identity.RoleAgent, "Applications"},
		{"Faisal Hassan", identity.RoleAgent, "Security"},
	}
	for i := 1; i <= 18; i++ {
		roster = append(roster, person{fmt.Sprintf("Employee %02d", i), identity.RoleRequester, ""})
	}

	var agents, requesters []*identity.User
	for i, p := range roster {
		var teamID *shared.ID
		if p.team != "" {
			id := teams[p.team]
			teamID = &id
		}
		email := fmt.Sprintf("%s%d@%s.test", roleSlug(p.role), i, slug)
		user, err := authService.InviteUser(ctx, admin, app.InviteUserInput{
			Email: email, FullName: p.name, Password: demoPassword, Role: p.role, TeamID: teamID,
		})
		if err != nil {
			return fmt.Errorf("creating %s: %w", p.name, err)
		}
		if p.role == identity.RoleRequester {
			requesters = append(requesters, user)
		} else {
			agents = append(agents, user)
		}
	}

	// --- assets ---------------------------------------------------------------
	assetKinds := []asset.Kind{asset.KindLaptop, asset.KindDesktop, asset.KindServer,
		asset.KindPrinter, asset.KindNetworkGear, asset.KindService}
	models := []string{"ThinkPad X1 Carbon", "MacBook Pro 14", "Dell OptiPlex 7010",
		"HP LaserJet Pro", "Cisco Catalyst 9200", "Payroll Service"}

	var assets []*asset.Asset
	for i := 0; i < 40; i++ {
		kind := assetKinds[i%len(assetKinds)]
		criticality := asset.CriticalityLow
		if kind == asset.KindServer || kind == asset.KindService {
			criticality = asset.CriticalityHigh
		} else if kind == asset.KindNetworkGear {
			criticality = asset.CriticalityMedium
		}

		var ownerID *shared.ID
		if kind == asset.KindLaptop || kind == asset.KindDesktop {
			ownerID = &requesters[i%len(requesters)].ID
		}

		created, err := assetService.Create(ctx, admin, app.CreateAssetInput{
			Tag:          fmt.Sprintf("AC-%04d", 1000+i),
			Name:         models[i%len(models)],
			Kind:         kind,
			Criticality:  criticality,
			Manufacturer: []string{"Lenovo", "Apple", "Dell", "HP", "Cisco", "Internal"}[i%6],
			Model:        models[i%len(models)],
			SerialNumber: fmt.Sprintf("SN%08d", rand.Intn(99999999)),
			Location:     []string{"Nairobi HQ — Floor 1", "Nairobi HQ — Floor 3", "Mombasa Branch", "Data Centre"}[i%4],
			OwnerID:      ownerID,
		})
		if err != nil {
			return fmt.Errorf("creating asset: %w", err)
		}
		assets = append(assets, created)
	}

	// --- tickets ---------------------------------------------------------------
	timelines, err := seedTickets(ctx, ticketService, approvalService, admin,
		agents, requesters, assets, teams, ticketCount)
	if err != nil {
		return err
	}

	// Spread the generated tickets back over the last month.
	if err := backdate(ctx, pool, admin.OrgID, timelines); err != nil {
		return fmt.Errorf("backdating: %w", err)
	}

	logger.Info("seed complete")
	fmt.Printf(`
Demo data ready.

  Admin     admin@%s.test
  Manager   manager0@%s.test
  Agent     agent1@%s.test
  Requester requester7@%s.test

  Password  %s   (every account)

Sign in at the web app, or:
  curl -s localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' \
    -d '{"email":"admin@%s.test","password":"%s"}'
`, slug, slug, slug, slug, demoPassword, slug, demoPassword)

	return nil
}

func roleSlug(role identity.Role) string {
	switch role {
	case identity.RoleManager:
		return "manager"
	case identity.RoleAgent:
		return "agent"
	default:
		return "requester"
	}
}

// timeline is the synthesised history of one seeded ticket: how long ago it
// was raised, and how long the desk spent on it. Both are decided up front
// because the *age* has to drive the *status* — see below.
type timeline struct {
	ticketID    shared.ID
	ageHours    int // hours ago the ticket was raised
	handlingHrs int // hours from raised to resolved; 0 while still open
}

// seedTickets generates a queue with a realistic shape: mostly resolved, a
// working set in progress, a handful breaching, and a few changes awaiting
// approval. A queue that is 100% open — or 100% closed — hides exactly the
// layout problems a demo is supposed to expose.
//
// The key decision is that a ticket's age is drawn first and then *determines*
// whether it is still open. Drawing the two independently — the obvious
// approach — produces a queue where a third of the open tickets are three
// weeks old, which is not what a functioning desk looks like and, because SLA
// budgets are measured in hours, marks nearly every open row as breaching. It
// also piles every open ticket onto today, giving the volume chart a single
// bar twenty times the height of the rest and flattening the month behind it.
func seedTickets(ctx context.Context, tickets *app.TicketService, approvals *app.ApprovalService,
	admin identity.Actor, agents, requesters []*identity.User,
	assets []*asset.Asset, teams map[string]shared.ID, count int) ([]timeline, error) {

	type template struct {
		kind    ticket.Kind
		subject string
		body    string
		impact  ticket.Impact
		urgency ticket.Urgency
		team    string
		tags    []string
	}

	templates := []template{
		{ticket.KindIncident, "Cannot connect to the VPN", "Getting 'authentication failed' since this morning. Tried restarting.", ticket.ImpactMedium, ticket.UrgencyHigh, "Network & Infrastructure", []string{"vpn", "connectivity"}},
		{ticket.KindIncident, "Payroll service is down", "The payroll portal returns a 502 for everyone in Finance.", ticket.ImpactHigh, ticket.UrgencyHigh, "Applications", []string{"payroll", "outage"}},
		{ticket.KindIncident, "Laptop will not boot", "Powers on, fan spins, black screen. Nothing on an external monitor either.", ticket.ImpactLow, ticket.UrgencyHigh, "End User Support", []string{"hardware"}},
		{ticket.KindIncident, "Printer on floor 3 jams constantly", "Every third job jams. Already cleared the tray twice today.", ticket.ImpactLow, ticket.UrgencyLow, "End User Support", []string{"printer"}},
		{ticket.KindIncident, "Wi-Fi keeps dropping in the boardroom", "Disconnects roughly every ten minutes during calls.", ticket.ImpactMedium, ticket.UrgencyMedium, "Network & Infrastructure", []string{"wifi"}},
		{ticket.KindIncident, "Shared drive is read-only", "Cannot save to the Finance share since the maintenance window.", ticket.ImpactMedium, ticket.UrgencyMedium, "Applications", []string{"storage", "permissions"}},
		{ticket.KindServiceRequest, "New starter setup — Operations", "Starting Monday. Needs a laptop, email, and access to the ops dashboard.", ticket.ImpactLow, ticket.UrgencyMedium, "End User Support", []string{"onboarding"}},
		{ticket.KindServiceRequest, "Request a second monitor", "Working across two spreadsheets constantly.", ticket.ImpactLow, ticket.UrgencyLow, "End User Support", []string{"hardware"}},
		{ticket.KindServiceRequest, "Access to the reporting database", "Read-only, for the quarterly board pack.", ticket.ImpactLow, ticket.UrgencyMedium, "Applications", []string{"access"}},
		{ticket.KindServiceRequest, "Install design software", "Need the licensed version for the brand refresh.", ticket.ImpactLow, ticket.UrgencyLow, "End User Support", []string{"software"}},
		{ticket.KindChange, "Upgrade core switch firmware", "Vendor advisory covers a remote DoS. Sunday 02:00 window proposed.", ticket.ImpactHigh, ticket.UrgencyMedium, "Network & Infrastructure", []string{"change", "firmware"}},
		{ticket.KindChange, "Rotate database credentials", "Quarterly rotation. Requires a rolling restart of the API tier.", ticket.ImpactHigh, ticket.UrgencyLow, "Security", []string{"change", "credentials"}},
		{ticket.KindChange, "Enable MFA for all staff", "Phased rollout starting with Finance and Engineering.", ticket.ImpactHigh, ticket.UrgencyMedium, "Security", []string{"change", "mfa"}},
		{ticket.KindProblem, "Recurring VPN drops on the Mombasa link", "Fifteen incidents this month all trace back to the same link.", ticket.ImpactMedium, ticket.UrgencyLow, "Network & Infrastructure", []string{"problem", "vpn"}},
		{ticket.KindProblem, "ThinkPad X1 battery failures", "Six units from the same batch have failed within a year.", ticket.ImpactLow, ticket.UrgencyLow, "End User Support", []string{"problem", "hardware"}},
	}

	replies := []string{
		"Thanks for reporting this — taking a look now.",
		"Could you confirm whether this happens on the wired network too?",
		"I have reproduced it. Escalating to the platform team.",
		"A workaround is in place while we investigate the root cause.",
		"This should be resolved now — please confirm at your end.",
	}
	internalNotes := []string{
		"Third report of this today. Possibly the same root cause as the Mombasa link.",
		"Vendor ticket raised, reference INC-88213.",
		"Do not restart the concentrator during business hours.",
		"Related to the change we shipped last Thursday.",
	}

	random := rand.New(rand.NewSource(42)) // fixed seed: reproducible demos
	timelines := make([]timeline, 0, count)

	const (
		windowHours   = 30 * 24 // how far back the history runs
		staleAfterHrs = 4 * 24  // beyond this, a ticket is expected to be finished
	)

	for i := 0; i < count; i++ {
		tpl := templates[random.Intn(len(templates))]
		requester := requesters[random.Intn(len(requesters))]
		requesterActor := requester.Actor()

		var assetIDs []shared.ID
		if random.Float64() < 0.6 {
			assetIDs = append(assetIDs, assets[random.Intn(len(assets))].ID)
		}
		teamID := teams[tpl.team]

		view, err := tickets.Create(ctx, requesterActor, app.CreateTicketInput{
			Kind: tpl.kind, Subject: tpl.subject, Description: tpl.body,
			Impact: tpl.impact, Urgency: tpl.urgency, TeamID: &teamID,
			Tags: tpl.tags, AssetIDs: assetIDs,
		})
		if err != nil {
			return nil, fmt.Errorf("creating ticket %d: %w", i, err)
		}
		t := view.Ticket

		// Creation dates are uniform across the window, so the volume chart
		// shows a steady stream rather than a spike.
		ageHours := random.Intn(windowHours)
		plan := timeline{ticketID: t.ID, ageHours: ageHours}
		// Anything older than a few days is expected to be finished — except
		// for a small fraction that stays open. Every real desk has a handful
		// of tickets that quietly got stuck, and they are precisely the ones
		// the breach highlight and the escalation path exist for; a seed where
		// nothing is ever late demonstrates neither.
		mustFinish := ageHours > staleAfterHrs && random.Float64() > 0.08

		// Pick an agent from the owning team where possible.
		var agent *identity.User
		for _, candidate := range agents {
			if candidate.TeamID != nil && *candidate.TeamID == teamID && candidate.Role == identity.RoleAgent {
				agent = candidate
				break
			}
		}
		if agent == nil {
			agent = agents[random.Intn(len(agents))]
		}
		agentActor := agent.Actor()

		// Distribution: ~8% untouched, ~7% triaged, ~9% in flight, ~6% waiting
		// on the requester, ~70% resolved or closed.
		//
		// The weighting toward "finished" is what a working desk actually looks
		// like, and it matters for more than realism: SLA budgets are measured
		// in hours, so a queue where most tickets stay open for weeks would show
		// almost every row as breaching — which is both untrue of a functioning
		// desk and useless as a demo, since the breach highlight would stop
		// distinguishing anything.
		roll := random.Float64()
		if mustFinish {
			// Squeeze the roll into the range that always ends in resolution.
			roll = 0.30 + random.Float64()*0.70
		}

		record := func() { timelines = append(timelines, plan) }

		if roll < 0.08 {
			record()
			continue
		}

		if _, err := tickets.Transition(ctx, agentActor, t.ID,
			app.TransitionInput{To: ticket.StatusTriaged}); err != nil {
			record()
			continue
		}
		if roll < 0.15 {
			record()
			continue
		}

		// Changes go through the real approval gate rather than being left in
		// triage. Two reasons: the demo should show the state that makes this
		// system ITSM rather than a helpdesk, and a change parked in `triaged`
		// keeps its SLA clock running — a queue of stale unapproved changes was
		// accounting for most of the seeded breaches, which is an artefact of
		// the seed rather than anything the desk did.
		if tpl.kind == ticket.KindChange {
			plan.ageHours = random.Intn(staleAfterHrs)

			if _, err := approvals.Request(ctx, agentActor, t.ID,
				[]shared.ID{approverFor(agents, agentActor.UserID)}); err != nil {
				record()
				continue
			}
			// Roughly half get a decision; the rest stay pending so the
			// approvals inbox has something in it.
			if random.Float64() < 0.5 {
				pending, err := approvals.Inbox(ctx, managerActor(agents))
				if err == nil {
					for _, request := range pending {
						if request.TicketID != t.ID {
							continue
						}
						_, _ = approvals.Decide(ctx, managerActor(agents), request.ID, app.DecideInput{
							Decision: approval.DecisionApproved,
							Comment:  "Reviewed — window agreed with operations.",
						})
					}
				}
			}
			record()
			continue
		}

		if _, err := tickets.Assign(ctx, agentActor, t.ID,
			app.AssignInput{AssigneeID: &agent.ID}); err != nil {
			record()
			continue
		}
		if _, err := tickets.Transition(ctx, agentActor, t.ID,
			app.TransitionInput{To: ticket.StatusInProgress}); err != nil {
			record()
			continue
		}

		_, _ = tickets.AddMessage(ctx, agentActor, t.ID, app.AddMessageInput{
			Body: replies[random.Intn(len(replies))], Visibility: ticket.VisibilityPublic,
		})
		if random.Float64() < 0.4 {
			_, _ = tickets.AddMessage(ctx, agentActor, t.ID, app.AddMessageInput{
				Body: internalNotes[random.Intn(len(internalNotes))], Visibility: ticket.VisibilityInternal,
			})
		}

		if roll < 0.24 {
			record()
			continue
		}
		if roll < 0.30 {
			_, _ = tickets.Transition(ctx, agentActor, t.ID,
				app.TransitionInput{To: ticket.StatusPendingRequester})
			record()
			continue
		}

		if _, err := tickets.Transition(ctx, agentActor, t.ID, app.TransitionInput{
			To:         ticket.StatusResolved,
			Resolution: strPtr(resolutions[random.Intn(len(resolutions))]),
		}); err != nil {
			record()
			continue
		}
		if roll > 0.55 {
			_, _ = tickets.Transition(ctx, agentActor, t.ID,
				app.TransitionInput{To: ticket.StatusClosed})
		}

		// Handling time, capped at the ticket's own age so it cannot be
		// resolved before it was raised — or in the future.
		handling := 1 + int(math.Pow(random.Float64(), 1.8)*40)
		if handling > ageHours {
			handling = maxInt(1, ageHours)
		}
		plan.handlingHrs = handling
		record()
	}

	_ = admin
	return timelines, nil
}

// approverFor picks a manager who is not the requesting agent — the domain
// rejects self-approval, and rightly so.
func approverFor(agents []*identity.User, requesterID shared.ID) shared.ID {
	for _, candidate := range agents {
		if candidate.Role == identity.RoleManager && candidate.ID != requesterID {
			return candidate.ID
		}
	}
	return shared.NilID
}

// managerActor returns the seeded manager, who holds the approval permission.
func managerActor(agents []*identity.User) identity.Actor {
	for _, candidate := range agents {
		if candidate.Role == identity.RoleManager {
			return candidate.Actor()
		}
	}
	return identity.Actor{}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var resolutions = []string{
	"Restarted the VPN concentrator and cleared the stale session table.",
	"Replaced the faulty RAM module; the unit has passed an overnight burn-in.",
	"Reissued the certificate and pushed it to the affected clients.",
	"Corrected the share permissions — the maintenance script had reset them.",
	"Replaced the printer's fuser assembly under warranty.",
	"Granted read-only access to the reporting replica and confirmed with the requester.",
}

func strPtr(s string) *string { return &s }

// backdate applies each ticket's synthesised timeline to its stored timestamps.
//
// This reaches past the repository and writes SQL directly, which is deliberate
// and worth explaining. TicketRepo.Update does not include created_at in its
// column list — a ticket's creation time is immutable, and that is correct: an
// audit trail whose timestamps the application can rewrite is not an audit
// trail. Seeding is the one legitimate exception, so it goes around the
// repository in a dev-only command rather than by adding a mutator to the
// repository that production code could reach.
//
// Without this, every seeded ticket is created "now": the volume chart is a
// single spike, no ticket has had time to breach, and the resolution-time
// percentiles are all zero — three of the four things the dashboard exists to
// report.
func backdate(ctx context.Context, pool *database.Pool, orgID shared.ID, plans []timeline) error {
	ids := make([]shared.ID, 0, len(plans))
	ages := make([]int32, 0, len(plans))
	handling := make([]int32, 0, len(plans))
	for _, plan := range plans {
		ids = append(ids, plan.ticketID)
		ages = append(ages, int32(plan.ageHours))
		handling = append(handling, int32(plan.handlingHrs))
	}

	// One statement over unnested arrays rather than a statement per ticket:
	// same clarity, one round trip.
	if _, err := pool.Querier(ctx).Exec(ctx, `
		WITH plan AS (
			SELECT * FROM unnest($2::uuid[], $3::int[], $4::int[])
			     AS t(id, age_hours, handling_hours)
		)
		UPDATE tickets t SET
			-- Creation moves back by the full age.
			created_at = now() - (p.age_hours * interval '1 hour'),
			-- Everything else is expressed relative to the new creation time,
			-- so the intervals between them are exactly the synthesised
			-- handling durations rather than the microseconds the seed
			-- actually took.
			first_response_at = CASE
				WHEN t.first_response_at IS NULL THEN NULL
				ELSE now() - (p.age_hours * interval '1 hour')
				     + (GREATEST(p.handling_hours, 1) * 0.25 * interval '1 hour')
			END,
			resolved_at = CASE
				WHEN t.resolved_at IS NULL THEN NULL
				ELSE now() - ((p.age_hours - p.handling_hours) * interval '1 hour')
			END,
			closed_at = CASE
				WHEN t.closed_at IS NULL THEN NULL
				ELSE now() - ((p.age_hours - p.handling_hours) * interval '1 hour')
			END,
			-- Deadlines are anchored to creation, so they shift with it.
			first_response_due = t.first_response_due - (p.age_hours * interval '1 hour'),
			resolution_due     = t.resolution_due     - (p.age_hours * interval '1 hour'),
			updated_at         = now() - ((p.age_hours - p.handling_hours) * interval '1 hour')
		FROM plan p
		WHERE p.id = t.id AND t.organization_id = $1`,
		orgID, ids, ages, handling); err != nil {
		return err
	}

	// Recompute the SLA outcome flags against the new timestamps. The
	// application sets these at the moment of resolving; after back-dating they
	// would otherwise all read "met", and 100% attainment on a seeded database
	// is the kind of number that makes a reviewer stop trusting the report.
	if _, err := pool.Querier(ctx).Exec(ctx, `
		UPDATE tickets SET
			resolution_met = CASE
				WHEN resolved_at IS NULL OR resolution_due IS NULL THEN NULL
				ELSE resolved_at <= resolution_due
			END,
			first_response_met = CASE
				WHEN first_response_at IS NULL OR first_response_due IS NULL THEN NULL
				ELSE first_response_at <= first_response_due
			END
		WHERE organization_id = $1`, orgID); err != nil {
		return err
	}

	// Messages move with their ticket so threads still read in order.
	if _, err := pool.Querier(ctx).Exec(ctx, `
		UPDATE ticket_messages m
		SET created_at = GREATEST(t.created_at, t.updated_at - interval '5 minutes')
		FROM tickets t
		WHERE t.id = m.ticket_id AND t.organization_id = $1`, orgID); err != nil {
		return err
	}

	// audit_log is deliberately left alone: migration 0002 revokes UPDATE on it
	// outright, and a seeding script is exactly the kind of code that guarantee
	// exists to stop. Its timestamps stay at seed time, which is honest — those
	// entries really were written now.

	var breaching int
	if err := pool.Querier(ctx).QueryRow(ctx, `
		SELECT count(*) FROM tickets
		WHERE organization_id = $1
		  AND status NOT IN ('resolved','closed','cancelled','pending_requester','pending_approval')
		  AND resolution_due < now()`, orgID).Scan(&breaching); err != nil {
		return err
	}

	fmt.Printf("  %d tickets spread over 30 days, %d now breaching their SLA\n", len(plans), breaching)
	return nil
}
