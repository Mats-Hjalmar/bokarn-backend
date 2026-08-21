// Package httpx is the HTTP adapter: all net/http, router, and OpenAPI wiring
// lives here. One *_endpoint.go file per subsystem registers its routes.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/account"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/assignment"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/authentication"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/authorization"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/availability"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/booking"
	appcache "github.com/Mats-Hjalmar/bokarn-backend/internal/cache"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/config"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/guest"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/inventory"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/logging"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/notify"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/otel"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/platform"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/pricing"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/joakimcarlsson/minmux/auth"
	"github.com/joakimcarlsson/minmux/openapi"
	"github.com/joakimcarlsson/minmux/outputcache"
	ocredis "github.com/joakimcarlsson/minmux/outputcache/redis"
	"github.com/joakimcarlsson/minmux/router"
	"github.com/redis/go-redis/v9"
)

var logger = logging.New("server")

// hostCacheTTL bounds how long a hostname keeps resolving to the operator it
// resolved to a moment ago. Slugs change rarely; a minute keeps a renamed or
// deleted operator from lingering for long.
const hostCacheTTL = time.Minute

// Server owns the router, OpenAPI generator, and the underlying http.Server.
type Server struct {
	cfg       config.Config
	router    *router.Router
	http      *http.Server
	db        *tenant.DB
	redis     *redis.Client
	kratos    *IdentityProbe
	cache     *outputcache.Cache
	tenants   *tenant.Store
	accounts  *account.Store
	authz     *authorization.Store
	inventory *inventory.Store
	avail     *availability.Store
	pricing   *pricing.Store
	bookings  *booking.Store
	platform  *platform.Store
}

// NewServer constructs a Server, registers all routes, and wires up the
// OpenAPI spec and docs endpoints.
func NewServer(
	cfg config.Config,
	db *tenant.DB,
	redisClient *redis.Client,
	platformStore *platform.Store,
) (*Server, error) {
	r := router.New()
	r.Use(otel.EnrichContext(r))
	r.Use(otel.Recover())

	gen := openapi.NewGenerator(openapi.Info{
		Title:       "bokarn API",
		Version:     "0.1.0",
		Description: "Booking and operations platform for campsites and resorts.",
	})

	s := &Server{
		cfg:       cfg,
		router:    r,
		db:        db,
		redis:     redisClient,
		kratos:    NewIdentityProbe(cfg.Kratos, cfg.Staff),
		tenants:   tenant.NewStore(db),
		accounts:  account.NewStore(db),
		authz:     authorization.NewStore(db),
		inventory: inventory.NewStore(db),
		avail:     availability.NewStore(db),
		pricing:   pricing.NewStore(db),
		platform:  platformStore,
	}
	s.bookings = booking.NewStore(
		db,
		assignment.NewStore(db),
		guest.NewStore(db),
		s.pricing,
		notify.NewStore(db),
	)

	staffResolver := authentication.New(
		cfg.Staff.PublicURL,
		authentication.AudienceStaff,
		cfg.Staff.SessionCacheTTL,
	)
	guestResolver := authentication.New(
		cfg.Kratos.PublicURL,
		authentication.AudienceGuest,
		cfg.Kratos.SessionCacheTTL,
	)

	authn := auth.New(r, auth.Config{
		Verifiers: map[string]auth.Verifier{
			staffScheme:    staffVerifier(staffResolver, s.accounts, s.authz),
			guestScheme:    guestVerifier(guestResolver),
			platformScheme: platformVerifier(staffResolver),
		},
	})

	hosts := tenant.NewHosts(db, hostCacheTTL)

	// The order below is the design, not a preference.
	//
	// Authentication runs first because a staff session is what names the
	// operator. Tenant resolution runs next, and must precede the cache: the
	// cache key is built from the request, and tenant.Cached reads the pinned
	// operator off its context. Registered the other way round, every operator
	// would share one cache entry per path.
	r.Use(authn.Middleware())
	r.Use(tenant.Middleware(tenantFromIdentity, hosts.Source()))
	r.Use(otel.CaptureUser())

	storage, err := appcache.Metered(
		ocredis.New(ocredis.Options{Client: redisClient}),
	)
	if err != nil {
		return nil, fmt.Errorf("build cache storage: %w", err)
	}

	s.cache = outputcache.New(r, outputcache.Config{
		Storage:         storage,
		DefaultDuration: time.Hour,
	})
	r.Use(s.cache.Middleware())

	registerSecuritySchemes(gen)
	s.registerHealth()
	s.registerMe()
	s.registerSites()
	s.registerAvailability()
	s.registerInventoryAdmin()
	s.registerQuotes()
	s.registerPricingAdmin()
	s.registerBookings()
	s.registerBookingAdmin()
	s.registerPlatform()
	registerSpec(r, gen)
	registerDocs(r)

	s.http = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           otel.Middleware(cfg.OTel.ServiceName)(r),
		ReadHeaderTimeout: 10 * time.Second,
	}

	return s, nil
}

// Addr is the address the server listens on.
func (s *Server) Addr() string { return s.http.Addr }

// Start begins serving and blocks until the server stops.
func (s *Server) Start() error { return s.http.ListenAndServe() }

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func isNoTenant(err error) bool {
	return errors.Is(err, tenant.ErrNoTenant)
}
