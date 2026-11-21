package luxmed

import "testing"

func TestBrowserRouteConfiguration(t *testing.T) {
	const one = `"123":{"api":"http://browser:8091","viewer":"http://browser:8092/vnc"}`
	const two = `"456":{"api":"http://second:8091","viewer":"http://second:8092/vnc"}`
	routes, err := ParseBrowserRoutes("{" + one + "," + two + "}")
	if err != nil || len(routes) != 2 || routes[123].API != "http://browser:8091" {
		t.Fatalf("routes = %v, err = %v", routes, err)
	}
	for _, raw := range []string{
		"{" + one + "," + one + "}",
		`{` + one + `,"456":{"api":"http://browser:8091","viewer":"http://browser:8092/vnc"}}`,
		`{` + one + `,"456":{"api":"http://browser:9091","viewer":"http://browser:9092/vnc"}}`,
		`{"456":{"api":"http://browser:8091","viewer":"http://second:8092/vnc"}}`,
		"{" + one + "} {}",
		"{}",
	} {
		if _, err := ParseBrowserRoutes(raw); err == nil {
			t.Errorf("invalid worker map accepted: %s", raw)
		}
	}
}

func TestSharedBrowserRoutesUseAccountScopedPaths(t *testing.T) {
	routes, err := SharedBrowserRoutes("http://browser:8091", "http://browser:8092/vnc", "123, 456")
	if err != nil || len(routes) != 2 {
		t.Fatalf("routes = %v, err = %v", routes, err)
	}
	if !routes[123].Shared || routes[123].API != "http://browser:8091/accounts/123" ||
		routes[456].Viewer != "http://browser:8092/accounts/456/vnc" {
		t.Fatalf("unscoped routes: %v", routes)
	}
	for _, invalid := range []string{"", "123,123", "00123", "../123", "9007199254740992"} {
		if _, err := SharedBrowserRoutes("http://browser:8091", "http://browser:8092/vnc", invalid); err == nil {
			t.Errorf("invalid shared browser account admitted: %q", invalid)
		}
	}
}
