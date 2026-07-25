package task

import "testing"

func TestIncrementalCheckpointFromMapLegacyRunID(t *testing.T) {
	// Legacy rows wrote run_id as a JSON number; it must still read as a
	// string so stale chains self-drop instead of matching "". (Values beyond
	// 2^53 already lost precision at write time — that loss is why run_id
	// became a string.)
	cp := IncrementalCheckpointFromMap(map[string]any{
		"run_id":        float64(1784941958872507),
		"next_gid":      "3364900",
		"scanned_count": float64(250),
		"latest_gid":    float64(3365000),
		"round":         float64(7),
	})
	if cp.RunID != "1784941958872507" {
		t.Errorf("legacy run_id: got %q", cp.RunID)
	}
	if cp.NextGID == nil || *cp.NextGID != "3364900" {
		t.Errorf("next_gid: got %v", cp.NextGID)
	}
	if cp.ScannedCount != 250 || cp.LatestGID != 3365000 || cp.Round != 7 {
		t.Errorf("counters: %+v", cp)
	}
}

func TestIncrementalCheckpointToMapShape(t *testing.T) {
	// Empty checkpoint must render explicit nulls (the shape the API and
	// frontend read), not omit keys.
	m := IncrementalCheckpoint{}.ToMap()
	for _, key := range []string{"run_id", "next_gid", "latest_gid"} {
		v, ok := m[key]
		if !ok {
			t.Errorf("%s missing", key)
		}
		if v != nil {
			t.Errorf("%s = %v, want nil", key, v)
		}
	}
	if m["scanned_count"] != 0 || m["round"] != 0 {
		t.Errorf("counters: %v", m)
	}

	cursor := "3364900"
	full := IncrementalCheckpoint{RunID: "abc", NextGID: &cursor, ScannedCount: 25, LatestGID: 9, Round: 2}.ToMap()
	if full["run_id"] != "abc" || full["next_gid"] != "3364900" || full["latest_gid"] != int64(9) {
		t.Errorf("full map: %v", full)
	}

	// Round-trip through the tolerant reader.
	back := IncrementalCheckpointFromMap(full)
	if back != (IncrementalCheckpoint{RunID: "abc", NextGID: back.NextGID, ScannedCount: 25, LatestGID: 9, Round: 2}) || *back.NextGID != cursor {
		t.Errorf("round-trip: %+v", back)
	}
}
