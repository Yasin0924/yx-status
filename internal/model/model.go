package model

// PingResult represents a latency measurement from this node to a target node/address.
type PingResult struct {
	TargetID  string  `json:"target_id"`           // e.g. "fr-oracle"
	TargetAddr string `json:"target_addr,omitempty"`// e.g. "fr.yxaiwen.com:443" (redacted or public)
	Method    string  `json:"method"`              // "tcp_connect"
	LatencyMs float64 `json:"latency_ms"`          // Round-trip time in milliseconds
	Status    string  `json:"status"`              // "ok", "timeout", "refused", "unreachable"
	CheckedAt int64   `json:"checked_at"`          // Unix timestamp
}

// NodeReport contains the metrics reported by a single agent node.
type NodeReport struct {
	NodeID      string       `json:"node_id"`
	Timestamp   int64        `json:"timestamp"`
	Sequence    uint64       `json:"sequence"`
	Hostname    string       `json:"hostname"`
	OS          string       `json:"os"`
	Arch        string       `json:"arch"`
	Uptime      uint64       `json:"uptime"` // Uptime in seconds
	CPUPercent  float64      `json:"cpu_percent"`
	MemTotal    uint64       `json:"mem_total"`     // Bytes
	MemUsed     uint64       `json:"mem_used"`      // Bytes
	MemAvail    uint64       `json:"mem_avail"`     // Bytes
	MemPercent  float64      `json:"mem_percent"`
	DiskTotal   uint64       `json:"disk_total"`    // Bytes
	DiskUsed    uint64       `json:"disk_used"`     // Bytes
	DiskPercent float64      `json:"disk_percent"`
	NetRxBytes  uint64       `json:"net_rx_bytes"`  // Total RX bytes
	NetTxBytes  uint64       `json:"net_tx_bytes"`  // Total TX bytes
	NetRxBps    float64      `json:"net_rx_bps"`    // RX rate in bytes/sec
	NetTxBps    float64      `json:"net_tx_bps"`    // TX rate in bytes/sec
	TCPConns    int          `json:"tcp_conns"`     // Active TCP connection count
	Load1       float64      `json:"load_1"`
	Load5       float64      `json:"load_5"`
	Load15      float64      `json:"load_15"`
	PingResults []PingResult `json:"ping_results,omitempty"`
}

// HistoryPoint is a compact historical metric point for sparklines.
type HistoryPoint struct {
	Timestamp  int64   `json:"t"`
	CPUPercent float64 `json:"cpu"`
	MemPercent float64 `json:"mem"`
	NetRxBps   float64 `json:"rx"`
	NetTxBps   float64 `json:"tx"`
}

// NodeMeta holds administrative configuration and metadata for a node.
type NodeMeta struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Region   string `json:"region"`
	Flag     string `json:"flag"`
	IsNAT    bool   `json:"is_nat"`
	Disabled bool   `json:"disabled,omitempty"`
}

// NodeState is the real-time aggregated state of a node.
type NodeState struct {
	Meta         NodeMeta       `json:"meta"`
	Status       string         `json:"status"` // "healthy", "warning", "offline"
	LastSeen     int64          `json:"last_seen"`
	LatestReport *NodeReport    `json:"latest_report,omitempty"`
	History      []HistoryPoint `json:"history,omitempty"` // Fixed capacity 60
}

// Summary aggregates overall cluster statistics.
type Summary struct {
	TotalNodes      int     `json:"total_nodes"`
	HealthyNodes    int     `json:"healthy_nodes"`
	WarningNodes    int     `json:"warning_nodes"`
	OfflineNodes    int     `json:"offline_nodes"`
	TotalCPUPercent float64 `json:"total_cpu_percent"`
	TotalMemUsed    uint64  `json:"total_mem_used"`
	TotalMemTotal   uint64  `json:"total_mem_total"`
	TotalMemPercent float64 `json:"total_mem_percent"`
	TotalNetRxBps   float64 `json:"total_net_rx_bps"`
	TotalNetTxBps   float64 `json:"total_net_tx_bps"`
}

// PingMatrixCell describes latency between source and target node.
type PingMatrixCell struct {
	SourceID  string  `json:"source_id"`
	TargetID  string  `json:"target_id"`
	LatencyMs float64 `json:"latency_ms"`
	Status    string  `json:"status"` // "ok", "timeout", "unreachable", "na_outbound"
	UpdatedAt int64   `json:"updated_at"`
}

// FullState is the complete system snapshot broadcast to WebSocket clients.
type FullState struct {
	Summary    Summary                   `json:"summary"`
	Nodes      map[string]*NodeState     `json:"nodes"`
	PingMatrix map[string]PingMatrixCell `json:"ping_matrix"` // key: "source->target"
	UpdatedAt  int64                     `json:"updated_at"`
}
