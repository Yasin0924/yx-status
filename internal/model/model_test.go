package model

import (
	"encoding/json"
	"testing"
)

func TestModel_Serialization(t *testing.T) {
	report := NodeReport{
		NodeID:      "sg-oracle-prod",
		Timestamp:   1773723000,
		Sequence:    1,
		Hostname:    "sg-node",
		OS:          "Linux",
		Arch:        "arm64",
		Uptime:      3600,
		CPUPercent:  25.5,
		MemTotal:    24000000000,
		MemUsed:     8000000000,
		MemAvail:    16000000000,
		MemPercent:  33.33,
		DiskTotal:   100000000000,
		DiskUsed:    40000000000,
		DiskPercent: 40.0,
		NetRxBytes:  500000,
		NetTxBytes:  300000,
		NetRxBps:    25000.0,
		NetTxBps:    15000.0,
		TCPConns:    42,
		Load1:       0.5,
		Load5:       0.4,
		Load15:      0.2,
		PingResults: []PingResult{
			{
				TargetID:  "fr-oracle",
				Method:    "tcp_connect",
				LatencyMs: 168.4,
				Status:    "ok",
				CheckedAt: 1773723000,
			},
		},
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("failed to marshal NodeReport: %v", err)
	}

	var parsed NodeReport
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal NodeReport: %v", err)
	}

	if parsed.NodeID != report.NodeID || parsed.CPUPercent != report.CPUPercent || len(parsed.PingResults) != 1 {
		t.Fatalf("unmarshaled mismatch: %+v", parsed)
	}
}

func TestFullState_Serialization(t *testing.T) {
	state := FullState{
		Summary: Summary{
			TotalNodes:   2,
			HealthyNodes: 2,
		},
		Nodes: map[string]*NodeState{
			"node-1": {
				Meta:   NodeMeta{ID: "node-1", Name: "Node 1", Region: "SG", Flag: "🇸🇬"},
				Status: "healthy",
			},
		},
		PingMatrix: map[string]PingMatrixCell{
			"node-1->node-2": {
				SourceID:  "node-1",
				TargetID:  "node-2",
				LatencyMs: 50.5,
				Status:    "ok",
			},
		},
		UpdatedAt: 1773723000,
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("failed to marshal FullState: %v", err)
	}

	var parsed FullState
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal FullState: %v", err)
	}

	if parsed.Summary.TotalNodes != 2 || parsed.Nodes["node-1"].Meta.Name != "Node 1" {
		t.Fatalf("mismatch in parsed FullState: %+v", parsed)
	}
}
