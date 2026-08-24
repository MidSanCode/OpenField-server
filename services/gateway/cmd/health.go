package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/database"
)

// probeTimeout caps how long the aggregate health check waits for one
// service. Unreachable services simply report down instead of stalling the
// whole response.
const probeTimeout = 3 * time.Second

// healthReport is the per-service section of the aggregate response.
type healthReport struct {
	Status    string            `json:"status"`
	LatencyMs int64             `json:"latency_ms"`
	Error     string            `json:"error,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

// healthHandler serves GET /api/v1/health (public): it fans out to every
// configured backend's /healthz and aggregates their status with latency, so
// clients can show which parts of the platform are currently working.
func healthHandler(cfg *config.Config) gin.HandlerFunc {
	services := []struct {
		name string
		url  string
	}{
		{"account", cfg.Services.Account},
		{"chat", cfg.Services.Chat},
		{"posts", cfg.Services.Posts},
		{"push", cfg.Services.Push},
		{"storage", cfg.Services.Storage},
	}

	client := &http.Client{Timeout: probeTimeout}

	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), probeTimeout+time.Second)
		defer cancel()

		reports := make(map[string]healthReport, len(services)+1)
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, svc := range services {
			if svc.url == "" {
				mu.Lock()
				reports[svc.name] = healthReport{Status: "unknown", Error: "not configured"}
				mu.Unlock()
				continue
			}
			wg.Add(1)
			go func(name, base string) {
				defer wg.Done()
				report := probeService(ctx, client, base)
				mu.Lock()
				reports[name] = report
				mu.Unlock()
			}(svc.name, svc.url)
		}

		// The gateway itself is up by definition (it answers this request);
		// its own database connectivity is probed inline.
		dbDetail := "up"
		if database.DB == nil {
			dbDetail = "n/a"
		} else if err := database.DB.PingContext(ctx); err != nil {
			dbDetail = "down: " + err.Error()
		}
		mu.Lock()
		reports["gateway"] = healthReport{
			Status:    "up",
			LatencyMs: 0,
			Details:   map[string]string{"database": dbDetail},
		}
		mu.Unlock()

		wg.Wait()

		allHealthy := true
		for _, r := range reports {
			if r.Status != "up" {
				allHealthy = false
			}
		}
		code := http.StatusOK
		if !allHealthy {
			code = http.StatusServiceUnavailable
		}
		c.JSON(code, gin.H{
			"all_healthy": allHealthy,
			"checked_at":  time.Now().UTC().Format(time.RFC3339Nano),
			"services":    reports,
		})
	}
}

// probeService GETs <base>/healthz and converts the result into a report.
func probeService(ctx context.Context, client *http.Client, base string) healthReport {
	start := time.Now()
	report := healthReport{}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		report.Status = "down"
		report.LatencyMs = time.Since(start).Milliseconds()
		report.Error = err.Error()
		return report
	}
	resp, err := client.Do(req)
	if err != nil {
		report.Status = "down"
		report.LatencyMs = time.Since(start).Milliseconds()
		report.Error = err.Error()
		return report
	}
	defer resp.Body.Close()
	report.LatencyMs = time.Since(start).Milliseconds()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var payload struct {
		Status  string            `json:"status"`
		Details map[string]string `json:"details"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Status != "" {
		report.Status = payload.Status
		report.Details = payload.Details
	} else if resp.StatusCode >= 500 {
		report.Status = "down"
		report.Error = "unexpected status code"
	} else {
		report.Status = "up"
	}
	return report
}
