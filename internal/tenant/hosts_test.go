package tenant

import "testing"

func TestSlugFromHost(t *testing.T) {
	cases := []struct {
		host string
		want string
		ok   bool
	}{
		// An operator's guest site.
		{"storsand.bokarn.localhost", "storsand", true},
		{"storsand.bokarn.se", "storsand", true},
		{"STORSAND.bokarn.se", "storsand", true},
		{"storsand.bokarn.localhost:3300", "storsand", true},

		// A tenant-pinned API call.
		{"storsand.api.bokarn.localhost", "storsand", true},

		// Service hostnames name no operator. Without this the dashboard's own
		// calls to the API would claim to be an operator called "api" and be
		// refused for disagreeing with the session.
		{"api.bokarn.localhost", "", false},
		{"dashboard.bokarn.localhost", "", false},
		{"auth-staff.bokarn.localhost", "", false},
		{"grafana.bokarn.localhost", "", false},
		{"mail.bokarn.localhost", "", false},
		{"www.bokarn.se", "", false},

		// Too few labels to carry one.
		{"localhost", "", false},
		{"localhost:1437", "", false},
		{"", "", false},
	}

	for _, c := range cases {
		got, ok := SlugFromHost(c.host)
		if got != c.want || ok != c.ok {
			t.Errorf("SlugFromHost(%q) = (%q, %v), want (%q, %v)",
				c.host, got, ok, c.want, c.ok)
		}
	}
}

// Every service hostname in deploy/dev-proxy.caddy must be reserved, or an
// operator could be created that the proxy shadows.
func TestReservedSlugsCoverTheProxyHostnames(t *testing.T) {
	for _, service := range []string{
		"api", "dashboard", "auth", "auth-admin", "auth-staff",
		"auth-staff-admin", "grafana", "otlp", "mail",
	} {
		if !SlugReserved(service) {
			t.Errorf("%q is a proxy hostname but not a reserved slug", service)
		}
	}
}
