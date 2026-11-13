package estate

import "testing"

// HS v2.09: discovery compares models without vendor prefixes, as the Python engine does.
func TestModelMatch(t *testing.T) {
	cases := []struct {
		planned, seen string
		want          bool
	}{
		{"DCS-7060X6-64PE", "DCS-7060X6-64PE-F", true},
		{"N9364E-SG2-O", "N9K-C9364E-SG2", true},
		{"SN5610", "MSN5610", true},
		{"7220 IXR-H5-64O", "Nokia 7220 IXR-H5-64O", true},
		{"QFX5240-64OD", "qfx5240-64od", true},
		{"DCS-7060X6-64PE", "DCS-7060X6-32PE", false},
		{"SN5610", "SN2201", false},
		{"", "SN2201", false},
	}
	for _, c := range cases {
		if got := modelMatch(c.planned, c.seen); got != c.want {
			t.Errorf("modelMatch(%q, %q) = %v, want %v", c.planned, c.seen, got, c.want)
		}
	}
}
