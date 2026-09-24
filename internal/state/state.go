package state

import (
	"sync"
	"time"

	"yx-status/internal/model"
)

const (
	RingBufferSize = 60
	HealthyTimeout = 10 // seconds
	WarningTimeout = 30 // seconds
)

// RingBuffer stores a fixed-capacity circular history of metric points.
type RingBuffer struct {
	items []model.HistoryPoint
	head  int
	size  int
	cap   int
}

// NewRingBuffer creates a circular buffer of fixed size.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = RingBufferSize
	}
	return &RingBuffer{
		items: make([]model.HistoryPoint, capacity),
		cap:   capacity,
	}
}

// Add appends a point, overwriting the oldest when full.
func (r *RingBuffer) Add(pt model.HistoryPoint) {
	if r.size < r.cap {
		r.items[r.size] = pt
		r.size++
	} else {
		r.items[r.head] = pt
		r.head = (r.head + 1) % r.cap
	}
}

// Slice returns all stored points in chronological order.
func (r *RingBuffer) Slice() []model.HistoryPoint {
	if r.size == 0 {
		return []model.HistoryPoint{}
	}
	res := make([]model.HistoryPoint, r.size)
	if r.size < r.cap {
		copy(res, r.items[:r.size])
		return res
	}
	// Filled ring buffer: oldest is at r.head
	n := copy(res, r.items[r.head:])
	copy(res[n:], r.items[:r.head])
	return res
}

// NodeRuntime tracks active telemetry and buffer for a single node.
type NodeRuntime struct {
	Meta         model.NodeMeta
	Status       string
	LastSeen     int64
	LatestReport *model.NodeReport
	History      *RingBuffer
}

// Manager coordinates all node states and computes snapshots.
type Manager struct {
	mu         sync.RWMutex
	nodes      map[string]*NodeRuntime
	pingMatrix map[string]model.PingMatrixCell
}

// NewManager creates a new thread-safe state manager.
func NewManager(initialNodes []model.NodeMeta) *Manager {
	m := &Manager{
		nodes:      make(map[string]*NodeRuntime),
		pingMatrix: make(map[string]model.PingMatrixCell),
	}
	for _, meta := range initialNodes {
		m.nodes[meta.ID] = &NodeRuntime{
			Meta:    meta,
			Status:  "offline",
			History: NewRingBuffer(RingBufferSize),
		}
	}
	return m
}

// RegisterNode adds or updates node metadata.
func (m *Manager) RegisterNode(meta model.NodeMeta) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if runtime, exists := m.nodes[meta.ID]; exists {
		runtime.Meta = meta
	} else {
		m.nodes[meta.ID] = &NodeRuntime{
			Meta:    meta,
			Status:  "offline",
			History: NewRingBuffer(RingBufferSize),
		}
	}
}

// UpdateReport applies a new incoming report from an agent.
func (m *Manager) UpdateReport(report *model.NodeReport) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().Unix()
	runtime, exists := m.nodes[report.NodeID]
	if !exists {
		// Dynamically auto-register unknown node with fallback meta
		runtime = &NodeRuntime{
			Meta: model.NodeMeta{
				ID:     report.NodeID,
				Name:   report.NodeID,
				Region: "Unknown",
				Flag:   "🌐",
			},
			History: NewRingBuffer(RingBufferSize),
		}
		m.nodes[report.NodeID] = runtime
	}

	runtime.LastSeen = now
	runtime.Status = "healthy"
	runtime.LatestReport = report

	// Append to 60-slice RingBuffer
	runtime.History.Add(model.HistoryPoint{
		Timestamp:  report.Timestamp,
		CPUPercent: report.CPUPercent,
		MemPercent: report.MemPercent,
		NetRxBps:   report.NetRxBps,
		NetTxBps:   report.NetTxBps,
	})

	// Update ping matrix
	for _, ping := range report.PingResults {
		cellKey := report.NodeID + "->" + ping.TargetID
		m.pingMatrix[cellKey] = model.PingMatrixCell{
			SourceID:  report.NodeID,
			TargetID:  ping.TargetID,
			LatencyMs: ping.LatencyMs,
			Status:    ping.Status,
			UpdatedAt: ping.CheckedAt,
		}
	}
}

// GetFullState builds a consistent FullState snapshot evaluating heartbeat debounce.
func (m *Manager) GetFullState() *model.FullState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now().Unix()

	summary := model.Summary{
		TotalNodes: len(m.nodes),
	}

	nodesMap := make(map[string]*model.NodeState, len(m.nodes))
	var totalActiveCPU float64
	var activeCount int

	for id, r := range m.nodes {
		// Debounced Heartbeat State Machine:
		// < 10s: Healthy
		// 10s - 30s: Warning
		// >= 30s: Offline
		var currentStatus string
		diff := now - r.LastSeen
		if r.LastSeen == 0 {
			currentStatus = "offline"
		} else if diff < HealthyTimeout {
			currentStatus = "healthy"
		} else if diff < WarningTimeout {
			currentStatus = "warning"
		} else {
			currentStatus = "offline"
		}

		switch currentStatus {
		case "healthy":
			summary.HealthyNodes++
		case "warning":
			summary.WarningNodes++
		case "offline":
			summary.OfflineNodes++
		}

		// Aggregate active node statistics (healthy or warning)
		if currentStatus != "offline" && r.LatestReport != nil {
			totalActiveCPU += r.LatestReport.CPUPercent
			summary.TotalMemUsed += r.LatestReport.MemUsed
			summary.TotalMemTotal += r.LatestReport.MemTotal
			summary.TotalNetRxBps += r.LatestReport.NetRxBps
			summary.TotalNetTxBps += r.LatestReport.NetTxBps
			activeCount++
		}

		nodesMap[id] = &model.NodeState{
			Meta:         r.Meta,
			Status:       currentStatus,
			LastSeen:     r.LastSeen,
			LatestReport: r.LatestReport,
			History:      r.History.Slice(),
		}
	}

	if activeCount > 0 {
		summary.TotalCPUPercent = totalActiveCPU / float64(activeCount)
	}
	if summary.TotalMemTotal > 0 {
		summary.TotalMemPercent = (float64(summary.TotalMemUsed) / float64(summary.TotalMemTotal)) * 100
	}

	// Copy PingMatrix
	matrixCopy := make(map[string]model.PingMatrixCell, len(m.pingMatrix))
	for k, v := range m.pingMatrix {
		matrixCopy[k] = v
	}

	return &model.FullState{
		Summary:    summary,
		Nodes:      nodesMap,
		PingMatrix: matrixCopy,
		UpdatedAt:  now,
	}
}
