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
