// Package notifier delivers email notifications.
//
// The guide calls notifications a mini-project of their own, and it is right —
// deliverability, retries, bounce handling and threading are each substantial.
// This implementation covers the honest subset: reliable SMTP delivery with
// bounded retries, and a logging fallback so a development environment is
// fully functional without an SMTP account.
//
// What is deliberately not here, and why: inbound email parsing (needs a
// provider webhook and a public URL), Message-ID threading (only matters once
// inbound exists), and per-user digest batching (a real feature, but one that
// needs usage data to tune). The EventPublisher seam means each can be added
// as another subscriber without touching the ticket service.
package notifier

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/blessnduta/ticketing-system/internal/app"
)

// LogNotifier writes notifications to the log instead of sending them. The
// default in development: a developer running the stack locally gets to see
// exactly what would have been sent, with no credentials and no risk of
// emailing a real person from a seeded database.
type LogNotifier struct{ logger *slog.Logger }

func NewLogNotifier(logger *slog.Logger) *LogNotifier { return &LogNotifier{logger: logger} }

var _ app.Notifier = (*LogNotifier)(nil)

func (n *LogNotifier) Notify(ctx context.Context, notification app.Notification) error {
	n.logger.InfoContext(ctx, "notification (not sent — no SMTP configured)",
		slog.String("to", notification.To),
		slog.String("subject", notification.Subject),
		slog.String("ticket_id", notification.TicketID.String()))
	return nil
}

// SMTPNotifier sends real mail.
type SMTPNotifier struct {
	host     string
	port     int
	username string
	password string
	from     string
	logger   *slog.Logger
}

func NewSMTPNotifier(host string, port int, username, password, from string, logger *slog.Logger) *SMTPNotifier {
	return &SMTPNotifier{
		host: host, port: port, username: username, password: password,
		from: from, logger: logger,
	}
}

var _ app.Notifier = (*SMTPNotifier)(nil)

const (
	maxSendAttempts = 3
	sendTimeout     = 15 * time.Second
)

// Notify sends with bounded retries and exponential backoff.
//
// Retries matter because transient SMTP failures are routine — greylisting in
// particular will reject a first attempt as a matter of policy. Three attempts
// with backoff clears greylisting without turning a genuinely down mail server
// into an unbounded retry storm.
func (n *SMTPNotifier) Notify(ctx context.Context, notification app.Notification) error {
	var lastErr error
	for attempt := 1; attempt <= maxSendAttempts; attempt++ {
		if err := n.send(ctx, notification); err != nil {
			lastErr = err
			n.logger.WarnContext(ctx, "notification send failed",
				slog.Int("attempt", attempt),
				slog.String("to", notification.To),
				slog.Any("error", err))

			if attempt < maxSendAttempts {
				backoff := time.Duration(1<<attempt) * time.Second
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("send notification after %d attempts: %w", maxSendAttempts, lastErr)
}

func (n *SMTPNotifier) send(ctx context.Context, notification app.Notification) error {
	address := net.JoinHostPort(n.host, fmt.Sprint(n.port))

	dialer := &net.Dialer{Timeout: sendTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}

	client, err := smtp.NewClient(conn, n.host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer func() { _ = client.Quit() }()

	// STARTTLS is required, not best-effort. Sending ticket contents — which
	// routinely include names, addresses and internal system details — over an
	// unencrypted connection is not acceptable, so a server that cannot
	// upgrade is an error rather than a silent downgrade.
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return fmt.Errorf("smtp server %s does not support STARTTLS", n.host)
	}
	if err := client.StartTLS(&tls.Config{ServerName: n.host, MinVersion: tls.VersionTLS12}); err != nil {
		return fmt.Errorf("starttls: %w", err)
	}

	if n.username != "" {
		auth := smtp.PlainAuth("", n.username, n.password, n.host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(n.from); err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	if err := client.Rcpt(notification.To); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := writer.Write([]byte(n.compose(notification))); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write message: %w", err)
	}
	return writer.Close()
}

// compose builds the RFC 5322 message.
//
// Header values are sanitised of CR and LF. Without that, a ticket subject
// containing a newline lets an attacker inject arbitrary headers — a Bcc to
// themselves, for instance — which is header injection, and ticket subjects
// are attacker-controlled by definition.
func (n *SMTPNotifier) compose(notification app.Notification) string {
	var message strings.Builder
	message.WriteString("From: " + sanitiseHeader(n.from) + "\r\n")
	message.WriteString("To: " + sanitiseHeader(notification.To) + "\r\n")
	message.WriteString("Subject: " + sanitiseHeader(notification.Subject) + "\r\n")
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	// A stable References header per ticket makes replies thread together in
	// the recipient's mail client rather than arriving as unrelated messages.
	message.WriteString(fmt.Sprintf("References: <ticket-%s@ticketing-system>\r\n", notification.TicketID))
	message.WriteString("\r\n")
	message.WriteString(notification.Body)
	message.WriteString("\r\n")
	return message.String()
}

func sanitiseHeader(value string) string {
	return strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(value)
}
