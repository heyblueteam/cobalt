package cobaltfile

import (
	"fmt"
	"strings"
	"testing"
)

func TestParsePrivateAddress(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, config string
		valid        bool
	}{
		{"stable public web", `"stablePublicWeb":true,"services":{"web":{"privateAddress":true}}`, true},
		{"shared web with sibling", `"stablePublicWeb":true,"stablePublicWebNetwork":"shared","services":{"web":{"privateAddress":true},"worker":{}}`, true},
		{"siblings without explicit shared network", `"stablePublicWeb":true,"services":{"web":{"privateAddress":true},"worker":{}}`, false},
		{"unsupported swarm parameters", `"stablePublicWeb":true,"services":{"web":{"privateAddress":true,"extraSwarmParams":"--network other"}}`, false},
		{"numbered web", `"services":{"web":{"privateAddress":true}}`, false},
		{"nonweb", `"stablePublicWeb":true,"services":{"web":{},"hook:deploy:start:after":{"type":"command","command":"true","privateAddress":true}}`, false},
		{"internal web", `"stablePublicWeb":true,"services":{"web":{"privateAddress":true,"exposedInternally":true}}`, false},
		{"static web", `"stablePublicWeb":true,"services":{"web":{"type":"static","privateAddress":true}}`, false},
		{"stop first web", `"stablePublicWeb":true,"services":{"web":{"privateAddress":true,"stopFirst":true}}`, false},
		{"published web", `"stablePublicWeb":true,"services":{"web":{"privateAddress":true,"publishedPorts":[{"publishedAs":8080,"fromContainerPort":8000}]}}`, false},
		{"exclusive volume web", `"stablePublicWeb":true,"services":{"web":{"privateAddress":true,"volumes":[{"name":"data","destinationPath":"/data"}]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cf, err := Parse([]byte(`{"version":"1.0",` + tc.config + `}`))
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				if !cf.Services["web"].PrivateAddress || cf.Services["web"].ExposedInternally || !cf.UsesStablePublicWeb() {
					t.Fatalf("unexpected parsed web: %+v", cf.Services["web"])
				}
			} else if err == nil || !strings.Contains(err.Error(), "privateAddress requires an eligible stablePublicWeb web service") {
				t.Fatalf("expected privateAddress validation error, got %v", err)
			}
		})
	}
}

func TestParseExposedInternallyCompatibility(t *testing.T) {
	t.Parallel()
	cf, err := Parse([]byte(`{"version":"1.0","stablePublicWeb":true,"services":{"web":{"exposedInternally":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cf.Services["web"].ExposedInternally || cf.Services["web"].PrivateAddress || cf.UsesStablePublicWeb() {
		t.Fatalf("unexpected internal service semantics: %+v", cf.Services["web"])
	}
}

func TestParseStopGracePeriodSeconds(t *testing.T) {
	t.Parallel()
	for _, serviceType := range []string{"container", "static", "generator", "command", "cron"} {
		for _, seconds := range []int{-1, 0, 1, 86400, 86401} {
			t.Run(fmt.Sprintf("%s/%d", serviceType, seconds), func(t *testing.T) {
				cf, err := Parse([]byte(fmt.Sprintf(`{"version":"1.0","services":{"web":{"type":%q,"command":"true","stopGracePeriodSeconds":%d}}}`, serviceType, seconds)))
				valid := seconds == 0 || (serviceType == "container" && seconds >= 1 && seconds <= 86400)
				if valid {
					if err != nil {
						t.Fatal(err)
					}
					if got := cf.Services["web"].StopGracePeriodSeconds; got != seconds {
						t.Fatalf("stop grace = %d, want %d", got, seconds)
					}
				} else if err == nil || !strings.Contains(err.Error(), "stopGracePeriodSeconds") {
					t.Fatalf("expected stop grace validation error, got %v", err)
				}
			})
		}
	}
	if got := Default().Services["web"].StopGracePeriodSeconds; got != 0 {
		t.Fatalf("default stop grace = %d", got)
	}
}
