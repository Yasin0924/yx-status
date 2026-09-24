package state

import (
	"testing"
	"time"

	"yx-status/internal/model"
)

func TestRingBuffer_OrderAndCapacity(t *testing.T) {
	rb := NewRingBuffer(5)
	if len(rb.Slice()) != 0 {
		t.Fatalf("expected empty slice, got %d", len(rb.Slice()))
	}

	// Add 3 items
	for i := 1; i <= 3; i++ {
		rb.Add(model.HistoryPoint{Timestamp: int64(i), CPUPercent: float64(i * 10)})
	}
	s := rb.Slice()
	if len(s) != 3 {
		t.Fatalf("expected 3 items, got %d", len(s))
	}
	if s[0].Timestamp != 1 || s[2].Timestamp != 3 {
		t.Fatalf("wrong order in partial slice: %+v", s)
	}

	// Add 4 more items (total 7, capacity 5 -> 1, 2 should be evicted; remaining: 3, 4, 5, 6, 7)
	for i := 4; i <= 7; i++ {
		rb.Add(model.HistoryPoint{Timestamp: int64(i), CPUPercent: float64(i * 10)})
	}
	s = rb.Slice()
	if len(s) != 5 {
		t.Fatalf("expected 5 items, got %d", len(s))
	}
	if s[0].Timestamp != 3 || s[4].Timestamp != 7 {
		t.Fatalf("expected items from 3 to 7, got: %+v", s)
	}
}

func TestHeartbeatStateMachine(t *testing.T) {
	mgr := NewManager([]model.NodeMeta{
		{ID: "node-healthy", Name: "Node Healthy"},
		{ID: "node-warning", Name: "Node Warning"},
		{ID: "node-offline", Name: "Node Offline"},
		{ID: "node-never", Name: "Node Never Seen"},
	})

	now := time.Now().Unix()

	// Update reports
	mgr.UpdateReport(&model.NodeReport{
		NodeID:     "node-healthy",
		Timestamp:  now,
		CPUPercent: 20.0,
		MemTotal:   1000,
		MemUsed:    400,
	})

	mgr.UpdateReport(&model.NodeReport{
		NodeID:     "node-warning",
		Timestamp:  now - 15,
		CPUPercent: 40.0,
		MemTotal:   1000,
		MemUsed:    600,
	})

	mgr.UpdateReport(&model.NodeReport{
		NodeID:     "node-offline",
		Timestamp:  now - 45,
		CPUPercent: 50.0,
		MemTotal:   1000,
		MemUsed:    800,
	})

	// Override lastSeen directly for testing timestamps
	mgr.mu.Lock()
	mgr.nodes["node-healthy"].LastSeen = now - 5
	mgr.nodes["node-warning"].LastSeen = now - 15
	mgr.nodes["node-offline"].LastSeen = now - 35
	mgr.mu.Unlock()

	fullState := mgr.GetFullState()

	if fullState.Nodes["node-healthy"].Status != "healthy" {
		t.Fatalf("expected healthy, got %s", fullState.Nodes["node-healthy"].Status)
	}
	if fullState.Nodes["node-warning"].Status != "warning" {
		t.Fatalf("expected warning, got %s", fullState.Nodes["node-warning"].Status)
	}
	if fullState.Nodes["node-offline"].Status != "offline" {
		t.Fatalf("expected offline, got %s", fullState.Nodes["node-offline"].Status)
	}
	if fullState.Nodes["node-never"].Status != "offline" {
		t.Fatalf("expected offline for never seen node, got %s", fullState.Nodes["node-never"].Status)
	}

	if fullState.Summary.HealthyNodes != 1 {
		t.Fatalf("expected 1 healthy node, got %d", fullState.Summary.HealthyNodes)
	}
	if fullState.Summary.WarningNodes != 1 {
		t.Fatalf("expected 1 warning node, got %d", fullState.Summary.WarningNodes)
	}
	if fullState.Summary.OfflineNodes != 2 {
		t.Fatalf("expected 2 offline nodes, got %d", fullState.Summary.OfflineNodes)
	}
}
