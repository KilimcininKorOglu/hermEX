package main

import (
	"bufio"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"hermex/internal/config"
)

// composeServiceLine matches a service key: two-space indent, a bare name, a colon.
var composeServiceLine = regexp.MustCompile(`^  ([a-z0-9][a-z0-9_-]*):\s*$`)

// composeServices reads the service names of the shipped compose file. A backend
// URL is reachable on the compose network only when its host is one of them.
func composeServices(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "hermex-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	services := map[string]bool{}
	inServices := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, " ") && line != "" {
			inServices = line == "services:"
			continue
		}
		if m := composeServiceLine.FindStringSubmatch(line); inServices && m != nil {
			services[m[1]] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return services
}

// wantComposeHost fails when a backend URL names a host the compose network does
// not resolve, which leaves the gateway answering 502 for that route.
func wantComposeHost(t *testing.T, services map[string]bool, field, raw string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || !services[u.Hostname()] {
		t.Errorf("%s = %q, whose host is not a compose service", field, raw)
	}
}

// TestDefaultBackendsAreComposeServices holds the built-in backend defaults to the
// service names hermex-compose.yml declares.
func TestDefaultBackendsAreComposeServices(t *testing.T) {
	services := composeServices(t)
	gw := resolveGateway(&config.Config{})
	for field, raw := range map[string]string{
		"mapi": gw.backendMapi, "ews": gw.backendEws, "activesync": gw.backendActiveSync,
		"dav": gw.backendDav, "webmail": gw.backendWebmail, "mta": gw.backendMta,
		"admin": gw.backendAdmin,
	} {
		wantComposeHost(t, services, field, raw)
	}
}

// TestExampleConfigBackendsAreComposeServices holds the shipped config template
// to the same names: an operator copies it as the starting config, so a backend it
// names wrongly is a route that fails on a fresh install.
func TestExampleConfigBackendsAreComposeServices(t *testing.T) {
	services := composeServices(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "docker", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	found := 0
	for key, v := range fields {
		if s, ok := v.(string); ok && strings.HasPrefix(key, "gateway_backend_") {
			found++
			wantComposeHost(t, services, key, s)
		}
	}
	if found == 0 {
		t.Fatal("the example config names no gateway backend")
	}
}
