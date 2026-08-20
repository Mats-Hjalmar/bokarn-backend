package httpx

import (
	"context"
	"net/http"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/config"
	"github.com/joakimcarlsson/minmux/openapi"
	"github.com/joakimcarlsson/minmux/router"
)

// HealthResponse is the body returned by the health probe.
type HealthResponse struct {
	Status   string `json:"status"   desc:"ok when every dependency answered"`
	Postgres string `json:"postgres" desc:"ok or error"`
	Redis    string `json:"redis"    desc:"ok or error"`
	Kratos   string `json:"kratos"   desc:"ok when both Kratos instances ready"`
}

// IdentityProbe reports readiness of the two Ory Kratos instances.
type IdentityProbe struct {
	urls   []string
	client *http.Client
}

// NewIdentityProbe builds a probe over the guest and staff Kratos admin APIs.
func NewIdentityProbe(guest, staff config.KratosConfig) *IdentityProbe {
	return &IdentityProbe{
		urls:   []string{guest.AdminURL, staff.AdminURL},
		client: &http.Client{Timeout: 2 * time.Second},
	}
}

// Ready returns nil when every configured instance answers its readiness probe.
func (p *IdentityProbe) Ready(ctx context.Context) error {
	for _, base := range p.urls {
		req, err := http.NewRequestWithContext(
			ctx, http.MethodGet, base+"/health/ready", nil,
		)
		if err != nil {
			return err
		}
		resp, err := p.client.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return &router.ProblemDetails{
				Status: resp.StatusCode,
				Title:  "kratos not ready",
				Detail: base,
			}
		}
	}
	return nil
}

func (s *Server) registerHealth() {
	s.router.Get("/api/v1/healthz", s.health,
		openapi.Summary("Health probe"),
		openapi.Description(
			"Reports whether Postgres, Redis and both Ory Kratos instances "+
				"are reachable. Returns 503 when any of them is not.",
		),
		openapi.Tags("Meta"),
		openapi.NoSecurity(),
		openapi.ReturnsBody[HealthResponse](
			http.StatusOK,
			"All dependencies healthy",
		),
		openapi.ReturnsBody[HealthResponse](
			http.StatusServiceUnavailable,
			"A dependency is unhealthy",
		),
	)
}

func (s *Server) health(c *router.Context) {
	ctx, cancel := context.WithTimeout(c.Ctx(), 3*time.Second)
	defer cancel()

	resp := HealthResponse{
		Status:   "ok",
		Postgres: "ok",
		Redis:    "ok",
		Kratos:   "ok",
	}
	status := http.StatusOK

	degrade := func(field *string, err error, what string) {
		logger.ErrorContext(ctx, what+" health check failed", "err", err)
		*field = "error"
		resp.Status = "unavailable"
		status = http.StatusServiceUnavailable
	}

	if err := s.db.Pool().Ping(ctx); err != nil {
		degrade(&resp.Postgres, err, "postgres")
	}
	if err := s.redis.Ping(ctx).Err(); err != nil {
		degrade(&resp.Redis, err, "redis")
	}
	if err := s.kratos.Ready(ctx); err != nil {
		degrade(&resp.Kratos, err, "kratos")
	}

	c.JSON(status, resp)
}
