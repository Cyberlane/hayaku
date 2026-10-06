package native

import (
	"github.com/Cyberlane/hayaku/internal/model"
	"testing"
)

func TestNativeCapabilityEffectsAreVetoOnly(t *testing.T) {
	for _, kind := range []string{"subprocess", "native", "network", "shared-state", "unknown", "clock", "random", "metadata"} {
		effect := model.Effect{Owner: "fixture", Kind: kind}
		if kind == "metadata" {
			effect.Path = "data"
		}
		if err := validateCapabilityEffects([]model.Effect{effect}); err == nil {
			t.Fatalf("unsupported capability %s admitted", kind)
		}
	}
	for _, effects := range [][]model.Effect{nil, {}, {{Owner: "fixture", Kind: "read", Path: "data"}}, {{Owner: "fixture", Kind: "exists", Path: "data"}}, {{Owner: "fixture", Kind: "directory", Path: "data"}}} {
		if err := validateCapabilityEffects(effects); err != nil {
			t.Fatal(err)
		}
	}
	for _, effect := range []model.Effect{{Owner: "", Kind: "read", Path: "data"}, {Owner: "fixture", Kind: "native", Path: "declared-safe"}, {Owner: "fixture", Kind: "typo"}, {Owner: string([]byte{255}), Kind: "read", Path: "data"}} {
		if err := validateCapabilityEffects([]model.Effect{effect}); err == nil {
			t.Fatal("malformed capability summary admitted")
		}
	}
	if len(capabilityPolicyDigest()) != 64 || capabilityPolicyDigest() == observablePolicyDigest() {
		t.Fatal("capability policy identity unavailable")
	}
}
