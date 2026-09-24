package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"yx-status/internal/auth"
	"yx-status/internal/hub"
	"yx-status/internal/model"
	"yx-status/internal/state"
)

// ServerConfig defines the configuration structure for the monitoring server.
type ServerConfig struct {
	Addr       string            `json:"addr"`
	MaxClients int               `json:"max_clients"`
	WebDir     string            `json:"web_dir"`
	Nodes      []NodeConfig      `json:"nodes"`
}

type NodeConfig struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
	Name   string `json:"name"`
	Region string `json:"region"`
	Flag   string `json:"flag"`
	IsNAT  bool   `json:"is_nat"`
}

func defaultServerConfig() ServerConfig {
	return ServerConfig{
		Addr:       ":8888",
		MaxClients: 2000,
		WebDir:     "web",
		// Node identities and their shared secrets must come from a local config file.
		Nodes: nil,
	}
}

func loadConfig(path string) (ServerConfig, error) {
	cfg := defaultServerConfig()
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("failed to parse config JSON: %w", err)
	}

	return cfg, nil
}

func main() {
	addrFlag := flag.String("addr", ":8888", "Server listen address (e.g. :8888)")
	configFlag := flag.String("config", "", "Path to server JSON configuration file")
	flag.Parse()

	cfg, err := loadConfig(*configFlag)
	if err != nil {
		log.Fatalf("[Server] Failed to load config: %v", err)
	}
	if len(cfg.Nodes) == 0 {
		log.Fatal("[Server] No nodes configured; provide a private config file")
	}
	for _, n := range cfg.Nodes {
		if n.ID == "" || n.Secret == "" {
			log.Fatal("[Server] Each node requires an ID and secret in the private config file")
		}
	}
	if *addrFlag != ":8888" || cfg.Addr == "" {
		cfg.Addr = *addrFlag
	}

	// Secret map & Node Meta list
	secretMap := make(map[string]string)
	var initialMetas []model.NodeMeta
	for _, n := range cfg.Nodes {
		secretMap[n.ID] = n.Secret
		initialMetas = append(initialMetas, model.NodeMeta{
			ID:     n.ID,
			Name:   n.Name,
			Region: n.Region,
			Flag:   n.Flag,
			IsNAT:  n.IsNAT,
		})
	}

	// 1. Auth Verifier
	verifier := auth.NewVerifier(func(nodeID string) (string, bool) {
		sec, ok := secretMap[nodeID]
		return sec, ok
	})

	// 2. State Manager
	stateMgr := state.NewManager(initialMetas)

	// 3. WebSocket Broadcast Hub
	wsHub := hub.NewHub(cfg.MaxClients)
	go wsHub.Run()

	// 4. Periodic WebSocket Broadcast Ticker (every 1 second)
	broadcastTicker := time.NewTicker(1 * time.Second)
	stopBroadcast := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopBroadcast:
				return
			case <-broadcastTicker.C:
				if wsHub.ClientCount() > 0 {
					currentState := stateMgr.GetFullState()
					data, err := json.Marshal(currentState)
					if err == nil {
						wsHub.Broadcast(data)
					}
				}
			}
		}
	}()

	// 5. HTTP Router setup
	mux := http.NewServeMux()

	// POST /api/v1/report
	mux.HandleFunc("POST /api/v1/report", func(w http.ResponseWriter, r *http.Request) {
		// Hard limit payload to 64KB to prevent memory exhaustion DoS
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			http.Error(w, "Error reading request body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		nodeID := r.Header.Get("X-Node-ID")
		timestampStr := r.Header.Get("X-Timestamp")
		signature := r.Header.Get("X-Signature")

		// Verify HMAC-SHA256 signature, clock skew & replay cache
		if err := verifier.Verify(nodeID, timestampStr, signature, body); err != nil {
			switch {
			case err == auth.ErrClockSkew:
				http.Error(w, "Clock skew outside ±30s", http.StatusBadRequest)
			case err == auth.ErrReplayDetected:
				http.Error(w, "Replay attack detected", http.StatusConflict)
			case err == auth.ErrUnknownNode || err == auth.ErrInvalidSignature:
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
			case err == auth.ErrMissingHeaders:
				http.Error(w, "Missing authentication headers", http.StatusBadRequest)
			default:
				http.Error(w, "Authentication failed", http.StatusForbidden)
			}
			return
		}

		var report model.NodeReport
		if err := json.Unmarshal(body, &report); err != nil {
			http.Error(w, "Invalid JSON report schema", http.StatusBadRequest)
			return
		}

		// Hard constraint: Payload node_id must strictly match the authenticated header node_id
		if report.NodeID != nodeID {
			http.Error(w, "Node ID mismatch between header and payload", http.StatusForbidden)
			return
		}

		// Update state in memory
		stateMgr.UpdateReport(&report)

		// Response per P0 architecture: return 204 No Content
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /api/v1/ws/live
	mux.HandleFunc("GET /api/v1/ws/live", func(w http.ResponseWriter, r *http.Request) {
		wsHub.HandleWS(w, r)
	})

	// GET /api/v1/state (HTTP polling fallback)
	mux.HandleFunc("GET /api/v1/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		currentState := stateMgr.GetFullState()
		_ = json.NewEncoder(w).Encode(currentState)
	})

	// Static Web directory hosting
	webDir := cfg.WebDir
	if _, err := os.Stat(webDir); os.IsNotExist(err) {
		_ = os.MkdirAll(webDir, 0755)
	}

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		// If requesting API paths that didn't match, return 404
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}

		targetFile := filepath.Join(webDir, filepath.Clean(r.URL.Path))
		info, err := os.Stat(targetFile)
		if err == nil && !info.IsDir() {
			http.ServeFile(w, r, targetFile)
			return
		}

		// Fallback to index.html (SPA routing)
		indexFile := filepath.Join(webDir, "index.html")
		if _, err := os.Stat(indexFile); err == nil {
			http.ServeFile(w, r, indexFile)
			return
		}

		http.NotFound(w, r)
	})

	server := &http.Server{
		Addr:           cfg.Addr,
		Handler:        mux,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 18, // 256 KB
	}

	// 6. Start server
	go func() {
		log.Printf("[Server] YxStatus Server listening on %s (WebDir: %s)", cfg.Addr, webDir)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[Server] ListenAndServe error: %v", err)
		}
	}()

	// 7. Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("[Server] Initiating graceful shutdown...")
	close(stopBroadcast)
	broadcastTicker.Stop()
	wsHub.Stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("[Server] Server forced to shutdown: %v", err)
	}

	log.Println("[Server] YxStatus Server stopped gracefully.")
}
