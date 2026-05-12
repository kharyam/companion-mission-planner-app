package kmz

import (
	"bytes"
	"testing"
)

func TestPlaceholderKMZRoundTrip(t *testing.T) {
	const lat, lng = 35.4286, -80.833
	raw, err := PlaceholderKMZ(lat, lng)
	if err != nil {
		t.Fatalf("PlaceholderKMZ: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("PlaceholderKMZ returned empty bytes")
	}

	// The placeholder must parse via our own ExtractMission so callers
	// (Registry.RegeneratePreview, KMZ inspect, etc.) don't blow up
	// when they encounter a cleared slot.
	m, err := ExtractMission(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("ExtractMission of placeholder: %v", err)
	}
	if got := len(m.Waypoints); got != 1 {
		t.Fatalf("placeholder waypoint count: got %d, want 1", got)
	}
	if m.Waypoints[0].Lat != lat || m.Waypoints[0].Lng != lng {
		t.Fatalf("placeholder waypoint coords: got (%f, %f), want (%f, %f)",
			m.Waypoints[0].Lat, m.Waypoints[0].Lng, lat, lng)
	}
}
