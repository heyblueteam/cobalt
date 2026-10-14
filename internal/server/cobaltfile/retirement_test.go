package cobaltfile

import (
	"fmt"
	"testing"
)

func TestWebRetirementPolicy(t *testing.T) {
	cf, err := Parse([]byte(`{"version":"1.0"}`))
	if err != nil || cf.WebGraceSeconds() != 300 || cf.MaxWebGenerations() != 2 {
		t.Fatalf("defaults: %+v %v", cf, err)
	}
	cf, err = Parse([]byte(`{"version":"1.0","webRetirement":{"gracePeriodSeconds":90,"maxGenerations":3}}`))
	if err != nil || cf.WebGraceSeconds() != 90 || cf.MaxWebGenerations() != 3 {
		t.Fatalf("custom: %+v %v", cf, err)
	}
	for _, policy := range []string{
		`{"gracePeriodSeconds":0}`, `{"gracePeriodSeconds":-1}`, `{"gracePeriodSeconds":59}`, `{"gracePeriodSeconds":86401}`,
		`{"maxGenerations":0}`, `{"maxGenerations":1}`, `{"maxGenerations":101}`, `{"maxGenerations":2.5}`, `{"typo":2}`,
	} {
		if _, err := Parse([]byte(fmt.Sprintf(`{"version":"1.0","webRetirement":%s}`, policy))); err == nil {
			t.Fatalf("accepted %s", policy)
		}
	}
}
