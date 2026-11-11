package domain

import "testing"

func TestCanTransition(t *testing.T) {
	allowed := [][2]State{
		{StateQueued, StateCreating},
		{StateCreating, StateConfiguring},
		{StateConfiguring, StateVerifying},
		{StateVerifying, StateReady},
		{StateQueued, StateDeleting},
		{StateCreating, StateDeleting},
		{StateConfiguring, StateDeleting},
		{StateVerifying, StateDeleting},
		{StateReady, StateDeleting},
		{StateCleanupPending, StateDeleting},
		{StateDeleting, StateDeleted},
		{StateCreating, StateCleanupPending},
		{StateConfiguring, StateCleanupPending},
		{StateVerifying, StateCleanupPending},
		{StateDeleting, StateCleanupPending},
		{StateCleanupPending, StateFailed},
	}
	for _, c := range allowed {
		if !CanTransition(c[0], c[1]) {
			t.Errorf("%s -> %s must be allowed", c[0], c[1])
		}
	}
	all := []State{StateQueued, StateCreating, StateConfiguring, StateVerifying, StateReady,
		StateDeleting, StateDeleted, StateCleanupPending, StateFailed}
	forbidden := make([][2]State, 0, 2+2*len(all))
	forbidden = append(forbidden, [2]State{StateReady, StateCreating}, [2]State{StateVerifying, StateQueued})
	for _, to := range all {
		forbidden = append(forbidden, [2]State{StateDeleted, to}, [2]State{StateFailed, to})
	}
	for _, c := range forbidden {
		if CanTransition(c[0], c[1]) {
			t.Errorf("%s -> %s must be forbidden", c[0], c[1])
		}
	}
}

func TestFinished(t *testing.T) {
	if !StateDeleted.Finished() || !StateFailed.Finished() || StateReady.Finished() {
		t.Fatal("Finished must be true only for DELETED and FAILED")
	}
}

func TestValidLanguage(t *testing.T) {
	for l, want := range map[string]bool{"python": true, "r": false, "R": false, "": false, "julia": false} {
		if got := ValidLanguage(l); got != want {
			t.Errorf("ValidLanguage(%q)=%v want %v", l, got, want)
		}
	}
}
