package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
)

var (
	ErrMissingHeaders   = errors.New("missing required auth headers")
	ErrClockSkew        = errors.New("timestamp outside ±30s clock window")
	ErrReplayDetected   = errors.New("replay attack detected: signature already used")
	ErrUnknownNode      = errors.New("unknown node id or node secret not configured")
	ErrInvalidSignature = errors.New("signature verification failed")
)

const (
	MaxClockSkewSeconds = 30
	ReplayTTL           = 60 * time.Second
	MaxReplayEntries    = 50000
)

// ComputeSignature calculates the canonical HMAC-SHA256 signature:
// Hex(HMAC_SHA256(NodeSecret, node_id + ":" + timestamp + ":" + SHA256(RequestBody)))
func ComputeSignature(secret string, nodeID string, timestamp int64, body []byte) string {
	bodyHash := sha256.Sum256(body)
	bodyHashHex := hex.EncodeToString(bodyHash[:])

	msg := fmt.Sprintf("%s:%d:%s", nodeID, timestamp, bodyHashHex)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

// replayItem holds the expiration time of a recorded signature key.
type replayItem struct {
	expiresAt time.Time
}

// ReplayCache tracks recently seen signatures to block replay attacks.
type ReplayCache struct {
	mu      sync.Mutex
	entries map[string]replayItem
	ttl     time.Duration
	maxCap  int
}

// NewReplayCache creates an in-memory anti-replay cache.
func NewReplayCache(ttl time.Duration, maxCap int) *ReplayCache {
	if ttl <= 0 {
		ttl = ReplayTTL
	}
	if maxCap <= 0 {
		maxCap = MaxReplayEntries
	}
	return &ReplayCache{
		entries: make(map[string]replayItem),
		ttl:     ttl,
		maxCap:  maxCap,
	}
}

// CheckAndRecord returns false if the key has already been seen and is not yet expired.
// If not seen, it records the key and returns true.
func (c *ReplayCache) CheckAndRecord(key string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 1. Check if key already exists and is active
	if item, exists := c.entries[key]; exists {
		if now.Before(item.expiresAt) {
			return false // Replay detected
		}
	}

	// 2. Proactive cleanup if reaching capacity threshold
	if len(c.entries) >= c.maxCap {
		for k, v := range c.entries {
			if now.After(v.expiresAt) {
				delete(c.entries, k)
			}
		}
		// If still over capacity, drop the oldest entries or clear half
		if len(c.entries) >= c.maxCap {
			dropped := 0
			for k := range c.entries {
				delete(c.entries, k)
				dropped++
				if dropped >= c.maxCap/4 {
					break
				}
			}
		}
	}

	// 3. Record new entry
	c.entries[key] = replayItem{
		expiresAt: now.Add(c.ttl),
	}
	return true
}

// CleanUp cleans expired entries manually.
func (c *ReplayCache) CleanUp(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.entries {
		if now.After(v.expiresAt) {
			delete(c.entries, k)
		}
	}
}

// Verifier handles validation of inbound node reports.
type Verifier struct {
	secretLookup func(nodeID string) (string, bool)
	replayCache  *ReplayCache
}

// NewVerifier initializes a Verifier with a secret lookup function.
func NewVerifier(secretLookup func(nodeID string) (string, bool)) *Verifier {
	return &Verifier{
		secretLookup: secretLookup,
		replayCache:  NewReplayCache(ReplayTTL, MaxReplayEntries),
	}
}

// Verify performs the complete verification pipeline:
// 1. Clock skew check (±30s)
// 2. Anti-replay cache check
// 3. Secret lookup
// 4. Constant-time signature comparison
func (v *Verifier) Verify(nodeID, timestampStr, signature string, body []byte) error {
	if nodeID == "" || timestampStr == "" || signature == "" {
		return ErrMissingHeaders
	}

	ts, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: invalid timestamp format", ErrClockSkew)
	}

	now := time.Now()
	nowSec := now.Unix()
	diff := nowSec - ts
	if diff < -MaxClockSkewSeconds || diff > MaxClockSkewSeconds {
		return ErrClockSkew
	}

	// Anti-replay check key: (NodeID, Timestamp, Signature)
	replayKey := fmt.Sprintf("%s:%d:%s", nodeID, ts, signature)
	if !v.replayCache.CheckAndRecord(replayKey, now) {
		return ErrReplayDetected
	}

	secret, exists := v.secretLookup(nodeID)
	if !exists || secret == "" {
		return ErrUnknownNode
	}

	expectedSig := ComputeSignature(secret, nodeID, ts, body)

	// Constant-time comparison to prevent timing attacks
	if subtle.ConstantTimeCompare([]byte(signature), []byte(expectedSig)) != 1 {
		return ErrInvalidSignature
	}

	return nil
}
