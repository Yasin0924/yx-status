package hub

import (
	"testing"
	"time"
)

func TestHub_SlowClientEviction(t *testing.T) {
	h := NewHub(10)
	go h.Run()
	defer h.Stop()

	// Create a dummy slow client with unconsumed send channel
	slowClient := &Client{
		hub:  h,
		send: make(chan []byte, clientSendBufferDepth),
	}

	h.register <- slowClient
	time.Sleep(10 * time.Millisecond)

	if h.ClientCount() != 1 {
		t.Fatalf("expected 1 client, got %d", h.ClientCount())
	}

	// Fill the buffer to capacity (depth 16)
	for i := 0; i < clientSendBufferDepth; i++ {
		h.broadcast <- []byte("msg")
	}
	time.Sleep(20 * time.Millisecond)

	// Now send one more message. Since slowClient's buffer is full, it must be evicted immediately.
	h.broadcast <- []byte("overflow_msg")
	time.Sleep(20 * time.Millisecond)

	if h.ClientCount() != 0 {
		t.Fatalf("expected slow client to be evicted, but client count is %d", h.ClientCount())
	}
}

func TestHub_MaxClientsLimit(t *testing.T) {
	max := 3
	h := NewHub(max)
	go h.Run()
	defer h.Stop()

	for i := 0; i < max+2; i++ {
		c := &Client{
			hub:  h,
			send: make(chan []byte, clientSendBufferDepth),
		}
		h.register <- c
	}

	time.Sleep(20 * time.Millisecond)

	if h.ClientCount() != max {
		t.Fatalf("expected exactly %d clients, got %d", max, h.ClientCount())
	}
}
