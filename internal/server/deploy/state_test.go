package deploy

import (
	"testing"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func TestStateClassifications(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s        cobaltapi.State
		active   bool
		terminal bool
	}{
		{cobaltapi.StateQueued, false, false},
		{cobaltapi.StateFetching, true, false},
		{cobaltapi.StateBuilding, true, false},
		{cobaltapi.StateSwapping, true, false},
		{cobaltapi.StateSuccess, false, true},
		{cobaltapi.StateFailed, false, true},
		{cobaltapi.StateCanceled, false, true},
		{cobaltapi.StateSkipped, false, true},
	}
	for _, c := range cases {
		t.Run(string(c.s), func(t *testing.T) {
			if got := c.s.IsActive(); got != c.active {
				t.Errorf("IsActive: got %v, want %v", got, c.active)
			}
			if got := c.s.IsTerminal(); got != c.terminal {
				t.Errorf("IsTerminal: got %v, want %v", got, c.terminal)
			}
			if !c.s.IsValid() {
				t.Errorf("IsValid: false for known state")
			}
		})
	}
}

func TestStateValidation_Unknown(t *testing.T) {
	t.Parallel()
	s := cobaltapi.State("rolling-back")
	if s.IsActive() {
		t.Error("unknown state should not be active")
	}
	if s.IsTerminal() {
		t.Error("unknown state should not be terminal")
	}
	if s.IsValid() {
		t.Error("unknown state should not be valid")
	}
}

func TestAllStatesAndActiveStatesList(t *testing.T) {
	t.Parallel()
	all := cobaltapi.AllStates()
	if len(all) != 8 {
		t.Errorf("AllStates: %d, want 8", len(all))
	}
	for _, s := range all {
		if !s.IsValid() {
			t.Errorf("AllStates contains invalid: %q", s)
		}
	}
	active := cobaltapi.ActiveStatesList()
	for _, s := range active {
		if !s.IsActive() {
			t.Errorf("ActiveStatesList contains non-active: %q", s)
		}
	}
}
