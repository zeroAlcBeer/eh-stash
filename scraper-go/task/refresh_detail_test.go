package task

import "testing"

func TestEnsureRefreshCheckpointResetsLegacyOrChangedScope(t *testing.T) {
	legacy := map[string]any{
		"offset":        float64(35575),
		"total_done":    float64(35197),
		"total_pending": float64(35596),
	}

	got, reset := ensureRefreshCheckpoint(legacy, 200, 32000, 48000)
	if !reset {
		t.Fatal("expected legacy checkpoint to reset")
	}
	if got["scope_min_fav"] != float64(200) {
		t.Fatalf("scope_min_fav = %v, want 200", got["scope_min_fav"])
	}
	if got["total_done"] != float64(32000) || got["total_pending"] != float64(48000) {
		t.Fatalf("unexpected reset totals: %#v", got)
	}
	if _, ok := got["offset"]; ok {
		t.Fatal("legacy offset must not survive reset")
	}
}

func TestEnsureRefreshCheckpointKeepsMatchingScopeAndRemovesOffset(t *testing.T) {
	checkpoint := map[string]any{
		"scope_min_fav": float64(200),
		"offset":        float64(25),
		"total_done":    float64(75),
		"total_pending": float64(25),
	}

	got, reset := ensureRefreshCheckpoint(checkpoint, 200, 80, 20)
	if reset {
		t.Fatal("matching scope should not reset")
	}
	if _, ok := got["offset"]; ok {
		t.Fatal("legacy offset must be removed")
	}
	if got["pass"] != float64(1) {
		t.Fatalf("pass = %v, want 1", got["pass"])
	}
	if got["total_done"] != float64(80) || got["total_pending"] != float64(20) {
		t.Fatalf("coverage should be refreshed from DB: %#v", got)
	}
}

func TestRefreshCursor(t *testing.T) {
	fav, gid := refreshCursor(map[string]any{
		"cursor_fav": float64(1234),
		"cursor_gid": float64(3800000),
	})
	if fav == nil || gid == nil || *fav != 1234 || *gid != 3800000 {
		t.Fatalf("unexpected cursor fav=%v gid=%v", fav, gid)
	}

	fav, gid = refreshCursor(map[string]any{})
	if fav != nil || gid != nil {
		t.Fatalf("empty checkpoint returned cursor fav=%v gid=%v", fav, gid)
	}
}

func TestCalcRefreshProgressUsesDonePlusRemaining(t *testing.T) {
	tests := []struct {
		name    string
		done    int
		pending int
		want    float64
	}{
		{name: "half", done: 50, pending: 50, want: 50},
		{name: "complete", done: 100, pending: 0, want: 100},
		{name: "empty", done: 0, pending: 0, want: 0},
		{name: "quarter", done: 25, pending: 75, want: 25},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := calcRefreshProgress(tt.done, tt.pending); got != tt.want {
				t.Fatalf("calcRefreshProgress(%d, %d) = %v, want %v", tt.done, tt.pending, got, tt.want)
			}
		})
	}
}
