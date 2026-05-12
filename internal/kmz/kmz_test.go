package kmz

import "testing"

func TestIsValidGUID(t *testing.T) {
	cases := map[string]bool{
		"550E8400-E29B-41D4-A716-446655440000": true,
		"550e8400-e29b-41d4-a716-446655440000": true,
		"550E8400E29B41D4A716446655440000":     false,
		"":                                     false,
		"not-a-guid":                           false,
	}
	for in, want := range cases {
		if got := IsValidGUID(in); got != want {
			t.Errorf("IsValidGUID(%q) = %v, want %v", in, got, want)
		}
	}
}
