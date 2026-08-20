package tenant

import (
	"net/http"
	"time"

	"github.com/joakimcarlsson/minmux/outputcache"
	"github.com/joakimcarlsson/minmux/router"
)

// Cached is the only permitted way to cache a tenant-scoped route.
//
// Two things have to be in the key and neither is there by default.
//
// The operator: minmux builds its key from method and path, so with
// per-operator hostnames GET /api/v1/sites is byte-identical across operators
// and Redis will hand one campsite's response to another without the database
// ever being consulted.
//
// The query string: it is *not* part of the default key either. A cached search
// route that does not name its parameters will serve July's availability to a
// December search. queryParams is therefore required rather than optional —
// pass every parameter that changes the response, or nil for a route that takes
// none. Getting this wrong is invisible in testing, because the first request
// of any run is always a miss and looks correct.
//
// Both only hold while Middleware runs before the cache middleware, since the
// key is derived from the request.
//
// Cache tags are fixed per route rather than per request, so a caller passing
// outputcache.Tags gets invalidation across every operator at once. There is no
// per-operator invalidation; a route that needs it wants a shorter TTL.
func Cached(d time.Duration, queryParams []string, opts ...any) router.Option {
	all := make([]any, 0, len(opts)+2)
	all = append(all, outputcache.VaryByCustom(func(r *http.Request) string {
		id, ok := MaybeFromContext(r.Context())
		if !ok {
			return "no-tenant"
		}
		return string(id)
	}))
	if len(queryParams) > 0 {
		all = append(all, outputcache.VaryByQuery(queryParams...))
	}
	all = append(all, opts...)
	return outputcache.WithOutputCache(d, all...)
}
