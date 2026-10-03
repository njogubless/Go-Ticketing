// Package realtime pushes ticket events to connected browsers over WebSocket.
//
// The security-critical part of this package is fan-out filtering: an event's
// audience is decided here, from data carried on the event itself, without a
// database round trip. Getting it wrong means one requester watching another
// requester's tickets in real time — which is a data breach that leaves no
// trace in any audit log, because nobody made a request for it.
package realtime

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

const (
	// writeWait bounds how long a single frame write may block. Without it, a
	// client on a stalled connection pins a goroutine indefinitely.
	writeWait = 10 * time.Second
	// pongWait must exceed pingPeriod; the read deadline is extended on each
	// pong, so a client that stops responding is reaped rather than lingering.
	pongWait   = 60 * time.Second
	pingPeriod = 45 * time.Second
	// maxMessageSize caps inbound frames. Clients only send subscription
	// control messages, so this is generous; the point is that it is bounded.
	maxMessageSize = 4096
	// sendBuffer per client. A client that falls this far behind is
	// disconnected rather than allowed to consume unbounded memory — a
	// dropped socket is recoverable, an OOM is not.
	sendBuffer = 32
)

// Client is one browser connection.
type Client struct {
	conn   *websocket.Conn
	actor  identity.Actor
	send   chan []byte
	hub    *Hub
	logger *slog.Logger
	once   sync.Once
}

// Hub owns the client set and the fan-out loop.
type Hub struct {
	mu sync.RWMutex
	// clients is indexed by organisation so a broadcast never even iterates
	// another tenant's connections. Tenant isolation as a data structure,
	// rather than as a condition inside a loop.
	clients map[shared.ID]map[*Client]struct{}
	logger  *slog.Logger
}

func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		clients: make(map[shared.ID]map[*Client]struct{}),
		logger:  logger,
	}
}

func (h *Hub) register(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[client.actor.OrgID] == nil {
		h.clients[client.actor.OrgID] = make(map[*Client]struct{})
	}
	h.clients[client.actor.OrgID][client] = struct{}{}
}

func (h *Hub) unregister(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if peers, ok := h.clients[client.actor.OrgID]; ok {
		delete(peers, client)
		if len(peers) == 0 {
			delete(h.clients, client.actor.OrgID)
		}
	}
}

// ConnectionCount reports live connections, for the health endpoint.
func (h *Hub) ConnectionCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	total := 0
	for _, peers := range h.clients {
		total += len(peers)
	}
	return total
}

// envelope is the wire format. Flat and versioned so the frontend can evolve
// independently of the server.
type envelope struct {
	Type     string         `json:"type"`
	TicketID string         `json:"ticket_id"`
	Payload  map[string]any `json:"payload,omitempty"`
	At       time.Time      `json:"at"`
}

// Broadcast delivers an event to every connection permitted to see it.
// Wired to the event bus in main.
func (h *Hub) Broadcast(event app.Event) {
	message, err := json.Marshal(envelope{
		Type:     string(event.Type),
		TicketID: event.TicketID.String(),
		Payload:  event.Payload,
		At:       event.At,
	})
	if err != nil {
		h.logger.Error("failed to encode realtime event", slog.Any("error", err))
		return
	}

	h.mu.RLock()
	peers := make([]*Client, 0, len(h.clients[event.OrgID]))
	for client := range h.clients[event.OrgID] {
		peers = append(peers, client)
	}
	h.mu.RUnlock()

	for _, client := range peers {
		if !visibleTo(client.actor, event) {
			continue
		}
		select {
		case client.send <- message:
		default:
			// The client is not draining. Disconnect it: the browser will
			// reconnect and refetch, which is strictly better than growing an
			// unbounded buffer for a connection that may already be dead.
			h.logger.Warn("realtime client too slow, disconnecting",
				slog.String("user_id", client.actor.UserID.String()))
			client.close()
		}
	}
}

// visibleTo is the fan-out authorisation rule.
//
// It mirrors app.authoriseTicketAccess, but works from the event's audience
// envelope instead of a loaded ticket — the fan-out path must not hit the
// database. The two are tested against each other, because a divergence here
// is a silent leak: nothing logs it and no request appears anywhere.
func visibleTo(actor identity.Actor, event app.Event) bool {
	if actor.OrgID != event.OrgID {
		return false
	}

	staff := actor.Can(identity.PermNoteInternal)

	// Internal-only events (internal notes, SLA machinery) never reach a
	// requester, even one who owns the ticket.
	if event.Audience.InternalOnly && !staff {
		return false
	}

	// Managers and admins see the whole organisation.
	if actor.Can(identity.PermTicketReadAll) {
		return true
	}

	// The requester sees their own ticket's public activity.
	if actor.UserID == event.Audience.RequesterID {
		return true
	}

	if staff {
		if event.Audience.AssigneeID != nil && *event.Audience.AssigneeID == actor.UserID {
			return true
		}
		// Unrouted tickets are visible to all staff, matching the query scope.
		if event.Audience.TeamID == nil {
			return true
		}
		if actor.InTeam(*event.Audience.TeamID) {
			return true
		}
	}

	return false
}

// ---------------------------------------------------------------------------
// Client pumps
// ---------------------------------------------------------------------------

// Attach takes ownership of an upgraded connection and starts its pumps.
func (h *Hub) Attach(conn *websocket.Conn, actor identity.Actor) {
	client := &Client{
		conn:   conn,
		actor:  actor,
		send:   make(chan []byte, sendBuffer),
		hub:    h,
		logger: h.logger,
	}
	h.register(client)
	go client.writePump()
	go client.readPump()
}

func (c *Client) close() {
	c.once.Do(func() {
		c.hub.unregister(c)
		close(c.send)
	})
}

// readPump exists mainly to detect a dead connection. Clients have nothing to
// tell the server over this socket — every mutation goes through the REST API,
// where it is authorised properly. Accepting commands here would mean a second
// authorisation surface, and second surfaces are where the gaps are.
func (c *Client) readPump() {
	defer func() {
		c.close()
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				c.logger.Debug("realtime connection closed unexpectedly", slog.Any("error", err))
			}
			return
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			// Pings keep the connection alive through proxies that reap idle
			// sockets, and are how a silently-dead peer is discovered.
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
