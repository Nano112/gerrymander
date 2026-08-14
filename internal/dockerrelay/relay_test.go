package dockerrelay

import (
	"testing"

	"github.com/Nano112/gerrymander/internal/core"
)

func TestRelayNamingDeterministic(t *testing.T) {
	d := core.DockerBackend{Network: "dev-proxy", Host: "olsyn-app", Port: 80}
	if RelayName(d) != RelayName(d) {
		t.Fatal("relay name not deterministic")
	}
	d2 := d
	d2.Port = 5175
	if RelayName(d) == RelayName(d2) {
		t.Fatal("distinct targets must get distinct relays")
	}
	if RelayOwner(d) == RelayOwner(d2) {
		t.Fatal("distinct targets must get distinct sticky-port owners")
	}
}
