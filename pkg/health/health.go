// Package health provides the shared liveness/readiness endpoint every
// service mounts at GET /healthz (internal port, no auth). The gateway's
// aggregate /api/v1/health fans out to these endpoints.
package health

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/database"
)

// Extra returns additional service-specific probe results merged into the
// response details (e.g. object-storage reachability). It must be quick and
// never panic; errors are reported, not propagated.
type Extra func(ctx context.Context) map[string]string

// Handler serves GET /healthz. Status is "up" when the shared database
// answers a ping within the timeout, "down" otherwise; services that do not
// use the shared database pool (e.g. push, which owns its own LISTEN
// connection) skip that check. HTTP always returns 200 so probes can read the
// body instead of just the code.
func Handler(extra Extra) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()

		details := make(map[string]string)
		status := "up"
		if database.DB == nil {
			details["database"] = "n/a"
		} else if err := database.DB.PingContext(ctx); err != nil {
			status = "down"
			details["database"] = "down: " + err.Error()
		} else {
			details["database"] = "up"
		}
		if extra != nil {
			for k, v := range extra(ctx) {
				details[k] = v
			}
			if v, ok := details["status"]; ok && v == "down" && status == "up" {
				status = "degraded"
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"status":     status,
			"details":    details,
			"checked_at": time.Now().UTC().Format(time.RFC3339Nano),
		})
	}
}
