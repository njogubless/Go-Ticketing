package notifier

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// Subscriber turns domain events into email notifications.
//
// It sits between the event bus and the Notifier so that the decision "which
// events deserve an email" lives in one readable place rather than scattered
// across the services that emit them. Getting this wrong in either direction
// is costly: too few notifications and tickets stall unnoticed; too many and
// agents filter the whole lot into a folder they never open, which is worse
// than sending none.
type Subscriber struct {
	notifier app.Notifier
	users    app.UserRepository
	tickets  app.TicketRepository
	baseURL  string
	logger   *slog.Logger
}

func NewSubscriber(notifier app.Notifier, users app.UserRepository, tickets app.TicketRepository,
	baseURL string, logger *slog.Logger) *Subscriber {
	return &Subscriber{
		notifier: notifier, users: users, tickets: tickets,
		baseURL: strings.TrimRight(baseURL, "/"), logger: logger,
	}
}

// Handle is registered on the event bus. It runs on a bus worker, off the
// request path, so a slow mail server cannot slow down the service desk.
func (s *Subscriber) Handle(ctx context.Context, event app.Event) {
	recipients, subject, body := s.compose(ctx, event)
	if len(recipients) == 0 {
		return
	}

	for _, recipient := range recipients {
		// Never notify the person who caused the event. Being emailed about
		// your own action is the fastest way to teach someone to ignore the
		// system's mail entirely.
		if event.ActorID != nil && recipient.ID == *event.ActorID {
			continue
		}
		if !recipient.Active {
			continue
		}
		if err := s.notifier.Notify(ctx, app.Notification{
			To:       recipient.Email,
			Subject:  subject,
			Body:     body,
			TicketID: event.TicketID,
			OrgID:    event.OrgID,
		}); err != nil {
			s.logger.ErrorContext(ctx, "notification delivery failed",
				slog.String("to", recipient.Email),
				slog.String("event_type", string(event.Type)),
				slog.Any("error", err))
		}
	}
}

type recipient struct {
	ID     shared.ID
	Email  string
	Active bool
}

// compose decides who hears about an event and what they are told.
func (s *Subscriber) compose(ctx context.Context, event app.Event) ([]recipient, string, string) {
	t, err := s.tickets.ByID(ctx, event.OrgID, event.TicketID)
	if err != nil {
		s.logger.ErrorContext(ctx, "notification could not load ticket",
			slog.String("ticket_id", event.TicketID.String()), slog.Any("error", err))
		return nil, "", ""
	}

	link := fmt.Sprintf("%s/tickets/%s", s.baseURL, t.ID)
	reference := t.Reference

	var (
		targets []shared.ID
		subject string
		body    string
	)

	switch event.Type {
	case app.EventTicketCreated:
		// New tickets notify nobody by email. On a busy desk this is pure
		// noise — the queue view and the realtime feed already surface it, and
		// an email per raised ticket is the classic reason people mute the
		// service desk's address.
		return nil, "", ""

	case app.EventTicketAssigned:
		if t.AssigneeID == nil {
			return nil, "", ""
		}
		targets = []shared.ID{*t.AssigneeID}
		subject = fmt.Sprintf("[%s] Assigned to you: %s", reference, t.Subject)
		body = fmt.Sprintf("You have been assigned %s (%s priority).\n\n%s\n\nOpen it: %s",
			reference, t.Priority, t.Subject, link)

	case app.EventTicketStatusChanged:
		// The requester is told when something has changed for them: a
		// resolution to confirm, or a question blocking progress.
		to, _ := event.Payload["to"].(string)
		switch to {
		case "resolved":
			targets = []shared.ID{t.RequesterID}
			subject = fmt.Sprintf("[%s] Resolved: %s", reference, t.Subject)
			resolution := ""
			if t.Resolution != nil {
				resolution = "\n\nWhat we did:\n" + *t.Resolution
			}
			body = fmt.Sprintf("Your ticket %s has been resolved.%s\n\n"+
				"If this isn't fixed, reply on the ticket and it will reopen: %s",
				reference, resolution, link)
		case "pending_requester":
			targets = []shared.ID{t.RequesterID}
			subject = fmt.Sprintf("[%s] We need your input: %s", reference, t.Subject)
			body = fmt.Sprintf("We're waiting on you to continue with %s.\n\n"+
				"The clock is paused until you reply: %s", reference, link)
		default:
			return nil, "", ""
		}

	case app.EventTicketMessageAdded:
		// Internal notes never generate mail to a requester. The event's
		// InternalOnly flag is the same one the realtime hub filters on.
		if event.Audience.InternalOnly {
			if t.AssigneeID == nil {
				return nil, "", ""
			}
			targets = []shared.ID{*t.AssigneeID}
			subject = fmt.Sprintf("[%s] Internal note added", reference)
		} else {
			targets = []shared.ID{t.RequesterID}
			if t.AssigneeID != nil {
				targets = append(targets, *t.AssigneeID)
			}
			subject = fmt.Sprintf("[%s] New reply: %s", reference, t.Subject)
		}
		preview, _ := event.Payload["preview"].(string)
		body = fmt.Sprintf("There's a new message on %s.\n\n%s\n\nRead and reply: %s",
			reference, preview, link)

	case app.EventTicketSLABreached:
		// Breaches go to the assignee. If nobody owns it, the breach is
		// exactly the signal that it needs an owner — but with no assignee
		// there is no individual to mail, so the realtime alert and the
		// breaching-queue view carry it instead.
		if t.AssigneeID == nil {
			return nil, "", ""
		}
		targets = []shared.ID{*t.AssigneeID}
		subject = fmt.Sprintf("[%s] SLA BREACHED — %s", reference, t.Subject)
		body = fmt.Sprintf("%s (%s) has passed its resolution target.\n\nOpen it now: %s",
			reference, t.Priority, link)

	case app.EventApprovalRequested:
		// Approvers are resolved by the approval service, which knows who was
		// nominated; the notification for them is raised there rather than
		// guessed at from the ticket.
		return nil, "", ""

	default:
		return nil, "", ""
	}

	return s.resolve(ctx, event.OrgID, targets), subject, body
}

func (s *Subscriber) resolve(ctx context.Context, orgID shared.ID, userIDs []shared.ID) []recipient {
	seen := map[shared.ID]struct{}{}
	recipients := make([]recipient, 0, len(userIDs))
	for _, userID := range userIDs {
		if _, duplicate := seen[userID]; duplicate {
			continue
		}
		seen[userID] = struct{}{}
		user, err := s.users.ByID(ctx, orgID, userID)
		if err != nil {
			continue
		}
		recipients = append(recipients, recipient{ID: user.ID, Email: user.Email, Active: user.Active})
	}
	return recipients
}
