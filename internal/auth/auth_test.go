package auth

import (
	"strconv"
	"testing"
	"time"
)

func TestAuth_Success(t *testing.T) {
	secretMap := map[string]string{
		"node-1": "test-secret-key-1234567890",
	}
	v := NewVerifier(func(nodeID string) (string, bool) {
		s, ok := secretMap[nodeID]
		return s, ok
	})

	nodeID := "node-1"
	now := time.Now().Unix()
	body := []byte(`{"node_id":"node-1","cpu_percent":12.5}`)

	sig := ComputeSignature(secretMap[nodeID], nodeID, now, body)
	err := v.Verify(nodeID, strconv.FormatInt(now, 10), sig, body)
	if err != nil {
		t.Fatalf("expected verification to pass, got: %v", err)
	}
}

func TestAuth_ReplayAttack(t *testing.T) {
	secretMap := map[string]string{
		"node-1": "secret-123",
	}
	v := NewVerifier(func(nodeID string) (string, bool) {
		s, ok := secretMap[nodeID]
		return s, ok
	})

	nodeID := "node-1"
	now := time.Now().Unix()
	body := []byte(`{"test":"payload"}`)
	sig := ComputeSignature(secretMap[nodeID], nodeID, now, body)

	// First attempt succeeds
	err := v.Verify(nodeID, strconv.FormatInt(now, 10), sig, body)
	if err != nil {
		t.Fatalf("first verification failed: %v", err)
	}

	// Immediate replay attempt must fail
	err = v.Verify(nodeID, strconv.FormatInt(now, 10), sig, body)
	if err != ErrReplayDetected {
		t.Fatalf("expected ErrReplayDetected, got: %v", err)
	}
}

func TestAuth_ClockSkew(t *testing.T) {
	secretMap := map[string]string{"node-1": "secret-123"}
	v := NewVerifier(func(nodeID string) (string, bool) {
		s, ok := secretMap[nodeID]
		return s, ok
	})

	nodeID := "node-1"
	body := []byte(`{"test":"payload"}`)

	// Expired (>30s in the past)
	oldTime := time.Now().Unix() - 35
	sigOld := ComputeSignature(secretMap[nodeID], nodeID, oldTime, body)
	err := v.Verify(nodeID, strconv.FormatInt(oldTime, 10), sigOld, body)
	if err != ErrClockSkew {
		t.Fatalf("expected ErrClockSkew for old timestamp, got: %v", err)
	}

	// Future (>30s in future)
	futureTime := time.Now().Unix() + 35
	sigFuture := ComputeSignature(secretMap[nodeID], nodeID, futureTime, body)
	err = v.Verify(nodeID, strconv.FormatInt(futureTime, 10), sigFuture, body)
	if err != ErrClockSkew {
		t.Fatalf("expected ErrClockSkew for future timestamp, got: %v", err)
	}
}

func TestAuth_TamperedBody(t *testing.T) {
	secretMap := map[string]string{"node-1": "secret-123"}
	v := NewVerifier(func(nodeID string) (string, bool) {
		s, ok := secretMap[nodeID]
		return s, ok
	})

	nodeID := "node-1"
	now := time.Now().Unix()
	body := []byte(`{"cpu": 10.0}`)
	sig := ComputeSignature(secretMap[nodeID], nodeID, now, body)

	tamperedBody := []byte(`{"cpu": 99.9}`)
	err := v.Verify(nodeID, strconv.FormatInt(now, 10), sig, tamperedBody)
	if err != ErrInvalidSignature {
		t.Fatalf("expected ErrInvalidSignature for tampered body, got: %v", err)
	}
}

func TestAuth_UnknownNode(t *testing.T) {
	v := NewVerifier(func(nodeID string) (string, bool) {
		return "", false
	})

	now := time.Now().Unix()
	err := v.Verify("node-x", strconv.FormatInt(now, 10), "fake-sig", []byte(`{}`))
	if err != ErrUnknownNode {
		t.Fatalf("expected ErrUnknownNode, got: %v", err)
	}
}

func TestAuth_MissingHeaders(t *testing.T) {
	v := NewVerifier(func(nodeID string) (string, bool) {
		return "secret", true
	})

	if err := v.Verify("", "12345", "sig", []byte{}); err != ErrMissingHeaders {
		t.Fatalf("expected ErrMissingHeaders, got: %v", err)
	}
	if err := v.Verify("node", "", "sig", []byte{}); err != ErrMissingHeaders {
		t.Fatalf("expected ErrMissingHeaders, got: %v", err)
	}
	if err := v.Verify("node", "12345", "", []byte{}); err != ErrMissingHeaders {
		t.Fatalf("expected ErrMissingHeaders, got: %v", err)
	}
}
