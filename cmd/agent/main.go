package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"yx-status/internal/auth"
	"yx-status/internal/model"
)

// AgentConfig holds agent configuration parameters.
type AgentConfig struct {
	ServerURL   string           `json:"server_url"`
	NodeID      string           `json:"node_id"`
	Secret      string           `json:"secret"`
	IntervalSec int              `json:"interval_sec"`
	PingTargets []PingTargetSpec `json:"ping_targets"`
}

type PingTargetSpec struct {
	ID   string `json:"id"`
	Addr string `json:"addr"` // e.g. "fr.yxaiwen.com:443"
}

func defaultAgentConfig() AgentConfig {
	return AgentConfig{
		ServerURL:   "http://127.0.0.1:8888",
		NodeID:      "",
		Secret:      "",
		IntervalSec: 2,
		PingTargets: []PingTargetSpec{
			{ID: "fr-oracle-prod", Addr: "127.0.0.1:8888"},
		},
	}
}

func loadAgentConfig(path string) (AgentConfig, error) {
	cfg := defaultAgentConfig()
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("failed to parse agent config: %w", err)
	}

	return cfg, nil
}

// SystemCollector collects read-only Linux /proc metrics without external commands.
type SystemCollector struct {
	prevCPUTotal uint64
	prevCPUIdle  uint64
	prevNetRx    uint64
	prevNetTx    uint64
	prevNetTime  time.Time
	sequence     uint64
}

func NewSystemCollector() *SystemCollector {
	return &SystemCollector{
		prevNetTime: time.Now(),
	}
}

// readCPU reads /proc/stat and calculates instantaneous CPU utilization percentage.
func (c *SystemCollector) readCPU() float64 {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) < 5 {
				return 0
			}

			var total uint64
			var idle uint64

			for i := 1; i < len(fields); i++ {
				val, _ := strconv.ParseUint(fields[i], 10, 64)
				total += val
				if i == 4 || i == 5 { // idle and iowait
					idle += val
				}
			}

			if c.prevCPUTotal == 0 {
				c.prevCPUTotal = total
				c.prevCPUIdle = idle
				return 0
			}

			deltaTotal := total - c.prevCPUTotal
			deltaIdle := idle - c.prevCPUIdle

			c.prevCPUTotal = total
			c.prevCPUIdle = idle

			if deltaTotal == 0 {
				return 0
			}

			percent := (1.0 - (float64(deltaIdle) / float64(deltaTotal))) * 100.0
			if percent < 0 {
				percent = 0
			} else if percent > 100 {
				percent = 100
			}
			return percent
		}
	}
	return 0
}

// readMemory reads /proc/meminfo to get MemTotal, MemAvailable, MemUsed.
func (c *SystemCollector) readMemory() (total, used, avail uint64, percent float64) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var memTotal, memFree, memAvail, buffers, cached uint64

	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		valField := strings.Fields(strings.TrimSpace(parts[1]))
		if len(valField) == 0 {
			continue
		}

		val, _ := strconv.ParseUint(valField[0], 10, 64)
		valBytes := val * 1024 // /proc/meminfo reports in kB

		switch key {
		case "MemTotal":
			memTotal = valBytes
		case "MemFree":
			memFree = valBytes
		case "MemAvailable":
			memAvail = valBytes
		case "Buffers":
			buffers = valBytes
		case "Cached":
			cached = valBytes
		}
	}

	if memTotal == 0 {
		return
	}

	// Follow Linux standard: MemTotal - MemAvailable
	if memAvail > 0 {
		used = memTotal - memAvail
		avail = memAvail
	} else {
		// Fallback for older Linux kernels
		avail = memFree + buffers + cached
		if memTotal > avail {
			used = memTotal - avail
		}
	}

	total = memTotal
	if total > 0 {
		percent = (float64(used) / float64(total)) * 100.0
	}
	return
}

// readDisk calls statfs on "/" to get disk capacity and utilization.
func (c *SystemCollector) readDisk() (total, used uint64, percent float64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return 0, 0, 0
	}

	total = stat.Blocks * uint64(stat.Bsize)
	avail := stat.Bavail * uint64(stat.Bsize)
	if total >= avail {
		used = total - avail
	}
	if total > 0 {
		percent = (float64(used) / float64(total)) * 100.0
	}
	return
}

// readNetwork reads /proc/net/dev to calculate network RX/TX totals and rates.
func (c *SystemCollector) readNetwork() (rxBytes, txBytes uint64, rxBps, txBps float64) {
	file, err := os.Open("/proc/net/dev")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		iface := strings.TrimSpace(parts[0])
		if iface == "lo" {
			continue // Skip loopback
		}

		fields := strings.Fields(parts[1])
		if len(fields) < 16 {
			continue
		}

		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)

		rxBytes += rx
		txBytes += tx
	}

	now := time.Now()
	deltaTime := now.Sub(c.prevNetTime).Seconds()
	if deltaTime > 0 && c.prevNetRx > 0 && c.prevNetTx > 0 {
		if rxBytes >= c.prevNetRx {
			rxBps = float64(rxBytes-c.prevNetRx) / deltaTime
		}
		if txBytes >= c.prevNetTx {
			txBps = float64(txBytes-c.prevNetTx) / deltaTime
		}
	}

	c.prevNetRx = rxBytes
	c.prevNetTx = txBytes
	c.prevNetTime = now
	return
}

// readLoad reads /proc/loadavg for system load averages.
func (c *SystemCollector) readLoad() (l1, l5, l15 float64) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return
	}
	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		l1, _ = strconv.ParseFloat(fields[0], 64)
		l5, _ = strconv.ParseFloat(fields[1], 64)
		l15, _ = strconv.ParseFloat(fields[2], 64)
	}
	return
}

// readTCPConns counts active TCP connections from /proc/net/tcp and /proc/net/tcp6.
func (c *SystemCollector) readTCPConns() int {
	countLines := func(path string) int {
		file, err := os.Open(path)
		if err != nil {
			return 0
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		count := 0
		for scanner.Scan() {
			count++
		}
		if count > 1 {
			return count - 1 // Exclude header line
		}
		return 0
	}
	return countLines("/proc/net/tcp") + countLines("/proc/net/tcp6")
}

// readUptime reads /proc/uptime.
func (c *SystemCollector) readUptime() uint64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) > 0 {
		val, _ := strconv.ParseFloat(fields[0], 64)
		return uint64(val)
	}
	return 0
}

// readHostname reads hostname from /proc/sys/kernel/hostname.
func (c *SystemCollector) readHostname() string {
	data, err := os.ReadFile("/proc/sys/kernel/hostname")
	if err == nil {
		return strings.TrimSpace(string(data))
	}
	h, _ := os.Hostname()
	return h
}

// readOS reads /etc/os-release for friendly OS description.
func (c *SystemCollector) readOS() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
		}
	}
	return runtime.GOOS
}

// ProbeTCP targets performs mild non-blocking TCP connect checks.
func probeTCPTargets(targets []PingTargetSpec) []model.PingResult {
	if len(targets) == 0 {
		return nil
	}

	results := make([]model.PingResult, len(targets))
	var wg sync.WaitGroup

	for i, t := range targets {
		wg.Add(1)
		go func(idx int, target PingTargetSpec) {
			defer wg.Done()
			now := time.Now()
			start := time.Now()
			conn, err := net.DialTimeout("tcp", target.Addr, 2*time.Second)

			res := model.PingResult{
				TargetID:  target.ID,
				Method:    "tcp_connect",
				CheckedAt: now.Unix(),
			}

			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					res.Status = "timeout"
				} else {
					res.Status = "unreachable"
				}
				res.LatencyMs = -1
			} else {
				res.LatencyMs = float64(time.Since(start).Microseconds()) / 1000.0
				res.Status = "ok"
				_ = conn.Close()
			}
			results[idx] = res
		}(i, t)
	}

	wg.Wait()
	return results
}

func main() {
	serverFlag := flag.String("server", "", "Server base URL (e.g. http://127.0.0.1:8888)")
	nodeIDFlag := flag.String("node-id", "", "Unique Node ID")
	secretFlag := flag.String("secret", "", "HMAC shared secret for this node")
	intervalFlag := flag.Int("interval", 0, "Reporting interval in seconds (default 2)")
	configFlag := flag.String("config", "", "Path to agent JSON config file")
	flag.Parse()

	cfg, err := loadAgentConfig(*configFlag)
	if err != nil && *configFlag != "" {
		log.Printf("[Agent] Warning: failed to load config from %s: %v. Using defaults.", *configFlag, err)
	}

	if *serverFlag != "" {
		cfg.ServerURL = *serverFlag
	}
	if *nodeIDFlag != "" {
		cfg.NodeID = *nodeIDFlag
	}
	if *secretFlag != "" {
		cfg.Secret = *secretFlag
	}
	if *intervalFlag > 0 {
		cfg.IntervalSec = *intervalFlag
	}
	if cfg.NodeID == "" || cfg.Secret == "" {
		log.Fatal("[Agent] Node ID and secret must be configured privately")
	}

	log.Printf("[Agent] Starting YxStatus Agent for node [%s], target server: %s", cfg.NodeID, cfg.ServerURL)

	collector := NewSystemCollector()
	httpClient := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Ping probe cache: probe targets every 30s to avoid network storm
	var cachedPings []model.PingResult
	var pingMu sync.Mutex
	lastPingProbe := time.Time{}

	reportURL := strings.TrimRight(cfg.ServerURL, "/") + "/api/v1/report"

	ticker := time.NewTicker(time.Duration(cfg.IntervalSec) * time.Second)
	defer ticker.Stop()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	hostname := collector.readHostname()
	osDesc := collector.readOS()
	arch := runtime.GOARCH

	for {
		select {
		case <-sigChan:
			log.Println("[Agent] Terminating cleanly on signal.")
			return

		case <-ticker.C:
			collector.sequence++
			now := time.Now()

			// Check if we should update TCP Ping Matrix probes (every 30s)
			if now.Sub(lastPingProbe) >= 30*time.Second {
				go func() {
					results := probeTCPTargets(cfg.PingTargets)
					pingMu.Lock()
					cachedPings = results
					lastPingProbe = time.Now()
					pingMu.Unlock()
				}()
			}

			pingMu.Lock()
			currentPings := make([]model.PingResult, len(cachedPings))
			copy(currentPings, cachedPings)
			pingMu.Unlock()

			// 1. Pure Read-Only Metric Harvesting
			cpuPct := collector.readCPU()
			memTotal, memUsed, memAvail, memPct := collector.readMemory()
			diskTotal, diskUsed, diskPct := collector.readDisk()
			netRx, netTx, rxBps, txBps := collector.readNetwork()
			l1, l5, l15 := collector.readLoad()
			tcpCount := collector.readTCPConns()
			uptime := collector.readUptime()

			report := model.NodeReport{
				NodeID:      cfg.NodeID,
				Timestamp:   now.Unix(),
				Sequence:    collector.sequence,
				Hostname:    hostname,
				OS:          osDesc,
				Arch:        arch,
				Uptime:      uptime,
				CPUPercent:  cpuPct,
				MemTotal:    memTotal,
				MemUsed:     memUsed,
				MemAvail:    memAvail,
				MemPercent:  memPct,
				DiskTotal:   diskTotal,
				DiskUsed:    diskUsed,
				DiskPercent: diskPct,
				NetRxBytes:  netRx,
				NetTxBytes:  netTx,
				NetRxBps:    rxBps,
				NetTxBps:    txBps,
				TCPConns:    tcpCount,
				Load1:       l1,
				Load5:       l5,
				Load15:      l15,
				PingResults: currentPings,
			}

			payloadBytes, err := json.Marshal(report)
			if err != nil {
				log.Printf("[Agent] Failed to marshal report: %v", err)
				continue
			}

			// 2. HMAC-SHA256 Signature Generation
			signature := auth.ComputeSignature(cfg.Secret, cfg.NodeID, report.Timestamp, payloadBytes)

			// 3. Unidirectional Push via HTTP POST
			reqCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, reportURL, bytes.NewReader(payloadBytes))
			if err != nil {
				cancel()
				log.Printf("[Agent] Failed to create HTTP request: %v", err)
				continue
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Node-ID", cfg.NodeID)
			req.Header.Set("X-Timestamp", strconv.FormatInt(report.Timestamp, 10))
			req.Header.Set("X-Signature", signature)

			resp, err := httpClient.Do(req)
			cancel()
			if err != nil {
				log.Printf("[Agent] Push failed: %v", err)
				continue
			}

			// 4. Response Discard Principle (P0 Hard Constraint):
			// Strictly read at most 1KB and discard. Do NOT parse any server commands/body!
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
			_ = resp.Body.Close()

			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
				log.Printf("[Agent] Server returned non-success code: %d", resp.StatusCode)
			}
		}
	}
}
