package scan

import (
	"errors"
	"testing"
)

func TestJobTransition(t *testing.T) {
	all := []State{StateQueued, StateRunning, StateDone, StateFailed, StateCancelled, StateExpired}
	allowed := map[[2]State]bool{
		{StateQueued, StateRunning}:    true,
		{StateQueued, StateDone}:       true,
		{StateQueued, StateFailed}:     true,
		{StateQueued, StateCancelled}:  true,
		{StateQueued, StateExpired}:    true,
		{StateRunning, StateDone}:      true,
		{StateRunning, StateFailed}:    true,
		{StateRunning, StateCancelled}: true,
	}
	for _, from := range all {
		for _, to := range all {
			j := Job{State: from}
			err := j.Transition(to)
			want := allowed[[2]State{from, to}]
			if want && err != nil {
				t.Errorf("%s → %s: unexpected error %v", from, to, err)
			}
			if want && j.State != to {
				t.Errorf("%s → %s: state is %s", from, to, j.State)
			}
			if !want && !errors.Is(err, ErrTransition) {
				t.Errorf("%s → %s: want ErrTransition, got %v", from, to, err)
			}
			if !want && j.State != from {
				t.Errorf("%s → %s: refused move changed the state to %s", from, to, j.State)
			}
		}
	}
}

func TestStateFinal(t *testing.T) {
	for s, want := range map[State]bool{
		StateQueued: false, StateRunning: false,
		StateDone: true, StateFailed: true, StateCancelled: true, StateExpired: true,
	} {
		if s.Final() != want {
			t.Errorf("%s.Final() = %v", s, !want)
		}
	}
}
