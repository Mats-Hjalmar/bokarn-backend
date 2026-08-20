package tenant

import (
	"encoding/json"
	"net/http"

	"github.com/joakimcarlsson/minmux/router"
)

// Source discovers the operator a request belongs to. It returns false when
// this source has nothing to say, and an error only when it found something
// malformed.
type Source func(*http.Request) (ID, bool, error)

// Middleware pins the operator for the rest of the request.
//
// The two sources are not interchangeable and their precedence is the security
// property: an authenticated staff caller's operator comes from their identity,
// and a Host naming a different operator is refused rather than obeyed. Were
// Host allowed to win, a token issued at one campsite would act on another
// simply by changing the hostname.
//
// A request that matches neither source proceeds with no tenant pinned. That is
// not a hole: every database path goes through DB.Tx, which fails with
// ErrNoTenant, and the policies themselves return nothing without a pin.
func Middleware(fromIdentity, fromHost Source) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, hasIdentity, err := fromIdentity(r)
			if err != nil {
				writeProblem(w, router.BadRequest(err.Error()))
				return
			}

			host, hasHost, err := fromHost(r)
			if err != nil {
				writeProblem(w, router.BadRequest(err.Error()))
				return
			}

			switch {
			case hasIdentity && hasHost && identity != host:
				pd := router.BadRequest(
					"the host names a different operator than this session",
				)
				pd.Type = "tenant_host_mismatch"
				writeProblem(w, pd)
				return
			case hasIdentity:
				r = r.WithContext(With(r.Context(), identity))
			case hasHost:
				r = r.WithContext(With(r.Context(), host))
			}

			next.ServeHTTP(w, r)
		})
	}
}

func writeProblem(w http.ResponseWriter, pd *router.ProblemDetails) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(pd.Status)
	_ = json.NewEncoder(w).Encode(pd)
}
