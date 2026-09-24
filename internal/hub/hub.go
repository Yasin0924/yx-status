package hub

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 5 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 30 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer (clients are purely listeners).
	maxMessageSize = 512

	// Channel buffer depth for each client per architecture spec.
	clientSendBufferDepth = 16

	// Default maximum concurrent WebSocket connections.
	DefaultMaxClients = 2000
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for public dashboard
	},
}

// Client represents an active WebSocket viewer.
type Client struct {
	hub       *Hub
	conn      *websocket.Conn
	send      chan []byte
	closeOnce sync.Once
	closed    bool
}

// Close gracefully closes the client channel and connection once.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		c.closed = true
		close(c.send)
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
}

// Hub maintains the set of active clients and broadcasts messages to them.
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
	maxClients int
	stopChan   chan struct{}
}

// NewHub creates a new WebSocket Hub with specified max client capacity.
func NewHub(maxClients int) *Hub {
	if maxClients <= 0 {
		maxClients = DefaultMaxClients
	}
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client, 64),
		unregister: make(chan *Client, 64),
		maxClients: maxClients,
		stopChan:   make(chan struct{}),
	}
}

// Run starts the Hub event loop.
func (h *Hub) Run() {
	for {
		select {
		case <-h.stopChan:
			h.mu.Lock()
			for client := range h.clients {
				delete(h.clients, client)
				client.Close()
			}
			h.mu.Unlock()
			return

		case client := <-h.register:
			h.mu.Lock()
			if len(h.clients) < h.maxClients {
				h.clients[client] = true
			} else {
				// Capacity reached, reject immediately
				client.Close()
			}
			h.mu.Unlock()

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.Close()
			}
			h.mu.Unlock()

		case message := <-h.broadcast:
			h.mu.Lock()
			// Slow consumer isolation: non-blocking write.
			// If buffer (depth 16) is full, evict immediately to prevent blocking the hub.
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					delete(h.clients, client)
					client.Close()
				}
			}
			h.mu.Unlock()
		}
	}
}

// Stop shuts down the hub and disconnects all clients.
func (h *Hub) Stop() {
	close(h.stopChan)
}

// Broadcast queues a message to be sent to all connected clients.
func (h *Hub) Broadcast(message []byte) {
	select {
	case h.broadcast <- message:
	default:
		// Drop broadcast message if hub buffer is full to prevent server stall
	}
}

// ClientCount returns current number of active connections.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// readPump pumps messages from the websocket connection to the hub.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
	}()
	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

// writePump pumps messages from the hub to the websocket connection.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.hub.unregister <- c
	}()
	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel (e.g. slow client eviction).
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			if _, err := w.Write(message); err != nil {
				return
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// HandleWS handles websocket upgrade requests from clients.
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	count := len(h.clients)
	h.mu.RUnlock()

	if count >= h.maxClients {
		http.Error(w, "Max WebSocket connections reached", http.StatusServiceUnavailable)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[Hub] upgrade failed: %v", err)
		return
	}

	client := &Client{
		hub:  h,
		conn: conn,
		send: make(chan []byte, clientSendBufferDepth),
	}

	h.register <- client

	// Start read/write pumps in separate goroutines
	go client.writePump()
	go client.readPump()
}
