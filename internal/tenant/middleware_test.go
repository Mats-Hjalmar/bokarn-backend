package tenant

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func fixed(id ID, ok bool) Source {
	return func(*http.Request) (ID, bool, error) { return id, ok, nil }
}

func TestMiddlewarePrecedence(t *testing.T) {
	const (
		a = ID("11111111-1111-1111-1111-111111111111")
		b = ID("22222222-2222-2222-2222-222222222222")
	)

	cases := []struct {
		name       string
		identity   Source
		host       Source
		wantStatus int
		wantTenant ID
		wantPinned bool
	}{
		{"identity only", fixed(a, true), fixed("", false), 200, a, true},
		{"host only", fixed("", false), fixed(b, true), 200, b, true},
		{
			"identity wins when they agree",
			fixed(a, true),
			fixed(a, true),
			200,
			a,
			true,
		},
		{"mismatch is refused", fixed(a, true), fixed(b, true), 400, "", false},
		{
			"neither leaves it unpinned",
			fixed("", false),
			fixed("", false),
			200,
			"",
			false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got ID
			var pinned bool
			h := Middleware(c.identity, c.host)(http.HandlerFunc(
				func(_ http.ResponseWriter, r *http.Request) {
					got, pinned = MaybeFromContext(r.Context())
				}))

			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(
				t.Context(), http.MethodGet, "/", nil)
			h.ServeHTTP(rec, req)

			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, c.wantStatus)
			}
			if pinned != c.wantPinned || got != c.wantTenant {
				t.Errorf(
					"tenant = %q pinned=%v, want %q pinned=%v",
					got, pinned, c.wantTenant, c.wantPinned,
				)
			}
		})
	}
}
