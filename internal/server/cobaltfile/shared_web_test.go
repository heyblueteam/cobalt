package cobaltfile

import (
	"strings"
	"testing"
)

func TestParseSharedWebSiblings(t *testing.T) {
	t.Parallel()
	for _, sibling := range []string{
		`{}`,
		`{"exposedInternally":true}`,
		`{"publishedPorts":[{"publishedAs":8080,"fromContainerPort":8000}]}`,
		`{"volumes":[{"name":"exclusive","destinationPath":"/data"}]}`,
		`{"extraSwarmParams":"--limit-cpu 1"}`,
		`{"type":"command","command":"true"}`,
	} {
		t.Run(sibling, func(t *testing.T) {
			cf, err := Parse([]byte(`{"version":"1.0","stablePublicWeb":true,"stablePublicWebNetwork":"shared","services":{"web":{},"worker":` + sibling + `}}`))
			if err != nil {
				t.Fatal(err)
			}
			if !cf.UsesStablePublicWeb() {
				t.Fatalf("shared web unexpectedly ineligible: %s", cf.StablePublicWebUnavailableReason())
			}
		})
	}
}

func TestParseSharedWebRequiresEligibleWeb(t *testing.T) {
	t.Parallel()
	for _, config := range []string{
		`"services":{"web":{}}`,
		`"stablePublicWeb":false,"services":{"web":{}}`,
		`"stablePublicWeb":true,"services":{}`,
		`"stablePublicWeb":true,"services":{"web":{"type":"static"}}`,
		`"stablePublicWeb":true,"services":{"web":{"exposedInternally":true}}`,
		`"stablePublicWeb":true,"services":{"web":{"stopFirst":true}}`,
		`"stablePublicWeb":true,"services":{"web":{"publishedPorts":[{"publishedAs":8080,"fromContainerPort":8000}]}}`,
		`"stablePublicWeb":true,"services":{"web":{"volumes":[{"name":"data","destinationPath":"/data"}]}}`,
		`"stablePublicWeb":true,"services":{"web":{"extraSwarmParams":"--network other"}}`,
		`"stablePublicWeb":true,"services":{"web":{},"worker":{"stopFirst":true}}`,
	} {
		_, err := Parse([]byte(`{"version":"1.0","stablePublicWebNetwork":"shared",` + config + `}`))
		if err == nil || !strings.Contains(err.Error(), "stablePublicWebNetwork") {
			t.Fatalf("config %s: expected explicit shared-network error, got %v", config, err)
		}
	}
}

func TestParseSharedWebNetworkEnum(t *testing.T) {
	t.Parallel()
	for _, value := range []string{`"unknown"`, `"Shared"`, `" shared"`, `true`, `1`, `{}`, `[]`} {
		if _, err := Parse([]byte(`{"version":"1.0","stablePublicWeb":true,"stablePublicWebNetwork":` + value + `,"services":{"web":{}}}`)); err == nil {
			t.Fatalf("accepted malformed/unknown network value %s", value)
		}
	}
}

func TestParseSharedWebCompatibility(t *testing.T) {
	t.Parallel()
	for _, network := range []string{``, `,"stablePublicWebNetwork":""`} {
		cf, err := Parse([]byte(`{"version":"1.0","stablePublicWeb":true` + network + `,"services":{"web":{},"worker":{"stopFirst":true}}}`))
		if err != nil {
			t.Fatal(err)
		}
		if cf.UsesStablePublicWeb() {
			t.Fatal("existing siblings must retain numbered fallback")
		}
		if !strings.Contains(cf.StablePublicWebUnavailableReason(), "stablePublicWebNetwork") {
			t.Fatalf("missing explicit opt-in guidance: %s", cf.StablePublicWebUnavailableReason())
		}
	}
	cf, err := Parse([]byte(`{"version":"1.0","stablePublicWeb":true,"services":{"web":{"stopFirst":true}}}`))
	if err != nil || cf.UsesStablePublicWeb() {
		t.Fatalf("legacy ineligible web must retain non-strict fallback: %v", err)
	}
	if _, err := Parse([]byte(`{"version":"1.0","stableProjectServices":true,"services":{"web":{}}}`)); err == nil || !strings.Contains(err.Error(), `unknown field "stableProjectServices"`) {
		t.Fatalf("removed prototype must be unknown, got %v", err)
	}
}
