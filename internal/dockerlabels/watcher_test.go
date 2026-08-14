package dockerlabels

import (
	"testing"

	"github.com/Nano112/gerrymander/internal/core"
)

func TestSplitByZones(t *testing.T) {
	zones := []core.Zone{{Name: "myapp.test"}, {Name: "app.myapp.test"}}
	for _, tc := range []struct{ host, zone, label string; ok bool }{
		{"api.myapp.test", "myapp.test", "api", true},
		{"x.app.myapp.test", "app.myapp.test", "x", true}, // longest zone wins
		{"myapp.test", "myapp.test", "@", true},
		{"api.other.test", "", "", false},
	} {
		z, l, ok := splitByZones(tc.host, zones)
		if z != tc.zone || l != tc.label || ok != tc.ok {
			t.Fatalf("%s → %q %q %v", tc.host, z, l, ok)
		}
	}
}
