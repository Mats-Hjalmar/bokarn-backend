package otel

import "context"

// requestInfo carries the per-request attributes that error and access events
// are tagged with: the matched route template and the authenticated user id. It
// is stored in the context by pointer so a middleware running after auth can
// populate userID while an outer middleware, running after the handler returns,
// still reads the same value — context values themselves only flow inward.
type requestInfo struct {
	route  string
	userID string
}

type requestInfoKeyType struct{}

var requestInfoKey requestInfoKeyType

// withRequestInfo attaches a fresh requestInfo holder to ctx and returns both
// the derived context and the holder. The outermost request middleware calls
// this once; downstream code mutates the holder through SetRoute/SetUser.
func withRequestInfo(ctx context.Context) (context.Context, *requestInfo) {
	info := &requestInfo{}
	return context.WithValue(ctx, requestInfoKey, info), info
}

func requestInfoFrom(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(requestInfoKey).(*requestInfo)
	return info
}

// SetRoute records the matched route template for the current request.
func SetRoute(ctx context.Context, route string) {
	if info := requestInfoFrom(ctx); info != nil {
		info.route = route
	}
}

// SetUser records the authenticated user id for the current request.
func SetUser(ctx context.Context, userID string) {
	if info := requestInfoFrom(ctx); info != nil {
		info.userID = userID
	}
}

// RouteAndUser returns the route template and user id recorded for the current
// request, empty strings when unset (no holder, unmatched route, or anonymous).
func RouteAndUser(ctx context.Context) (route, userID string) {
	if info := requestInfoFrom(ctx); info != nil {
		return info.route, info.userID
	}
	return "", ""
}
