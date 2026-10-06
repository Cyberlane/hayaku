package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/graph"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

func TestNativeObservablePolicyVeto(t *testing.T) {
	for _, kind := range []string{"metadata", "clock", "random", "unknown"} {
		path := ""
		if kind == "metadata" {
			path = "data/value"
		}
		if err := validateObservableEffects([]model.Effect{{Owner: "unit", Kind: kind, Path: path}}); err == nil {
			t.Fatalf("observable %s accepted", kind)
		}
	}
	// These are valid summaries for this veto only, not evidence of complete
	// semantics or enforcement. K09 and providers independently handle them.
	for _, kind := range []string{"read", "directory", "exists", "network", "subprocess", "shared-state", "native"} {
		path := ""
		if kind == "read" || kind == "directory" || kind == "exists" {
			path = "data/value"
		}
		if err := validateObservableEffects([]model.Effect{{Owner: "unit", Kind: kind, Path: path}}); err != nil {
			t.Fatalf("well-formed descriptive %s summary rejected: %v", kind, err)
		}
	}
	if err := validateObservableEffects(nil); err != nil {
		t.Fatal(err)
	}
	if err := validateObservableEffects([]model.Effect{}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeObservablePolicyRejectsMalformedData(t *testing.T) {
	for _, effect := range []model.Effect{
		{Kind: "read", Path: "data/value"},
		{Owner: "unit", Kind: "read"},
		{Owner: "unit", Kind: "invented"},
		{Owner: "unit", Kind: "read", Path: "../outside"},
		{Owner: "unit", Kind: "read", Path: "/outside"},
		{Owner: "unit", Kind: "read", Path: "a\\b"},
		{Owner: "unit", Kind: "read", Path: "a:b"},
		{Owner: "unit", Kind: "network", Path: "declared"},
		{Owner: "unit\x00", Kind: "read", Path: "data/value"},
		{Owner: "unit\r", Kind: "read", Path: "data/value"},
		{Owner: "unit\n", Kind: "read", Path: "data/value"},
		{Owner: string([]byte{255}), Kind: "read", Path: "data/value"},
		{Owner: "unit", Kind: "read", Path: "data/\x00"},
		{Owner: "unit", Kind: "read", Path: "data/\r"},
		{Owner: "unit", Kind: "read", Path: "data/\n"},
		{Owner: "unit", Kind: "read", Path: string([]byte{255})},
		{Owner: "unit", Kind: string([]byte{255}), Path: "data/value"},
		{Owner: strings.Repeat("u", 8193), Kind: "read", Path: "data/value"},
		{Owner: "unit", Kind: "read", Path: strings.Repeat("p", 4097)},
		{Owner: "unit", Kind: strings.Repeat("k", 65)},
	} {
		if err := validateObservableEffects([]model.Effect{effect}); err == nil {
			t.Fatal("malformed effect accepted")
		}
	}
}

func TestNativeObservablePolicyBoundsRepeatedSummaries(t *testing.T) {
	if err := validateObservableEffects(make([]model.Effect, graph.MaxEdges+1)); err == nil {
		t.Fatal("unbounded effect count accepted")
	}
	// Repeated equivalent rows must still consume the aggregate budget. No
	// deduplication can hide oversized or incomplete input evidence.
	effects := make([]model.Effect, (16<<20)/8192+1)
	for i := range effects {
		effects[i] = model.Effect{Owner: strings.Repeat("u", 8192), Kind: "read", Path: "data/value"}
	}
	if err := validateObservableEffects(effects); err == nil {
		t.Fatal("oversized repeated summary accepted")
	}
}

func TestNativeObservableSummaryNeverCreatesProof(t *testing.T) {
	// A validator's success is explicitly insufficient for execution authority.
	a := Artifact{Name: "unit", Cases: []string{"case"}, Effects: []model.Effect{{Owner: "unit", Kind: "read", Path: "data/value"}}}
	if validateObservableEffects(a.Effects) != nil {
		t.Fatal("invalid fixture")
	}
	cache := filepath.Join(t.TempDir(), "cache")
	r, err := Execute(context.Background(), Backend{Identity: strings.Repeat("a", 64)}, a, cache, false, func(process.Output, Artifact) (model.Outcomes, error) {
		t.Fatal("descriptive effect data reached execution decoder")
		return model.Outcomes{}, nil
	})
	if err == nil || err.Error() != "native artifact is not qualified" || r.Qualified || r.Complete || r.Passed || r.Executed || r.Reused || r.Key != "" {
		t.Fatalf("effect summary authorized execution: %+v %v", r, err)
	}
	if _, err := os.Lstat(cache); !os.IsNotExist(err) {
		t.Fatal("descriptive summary opened cache authority")
	}
	if observablePolicyDigest() == digest([]byte(contractVersion)) || len(observablePolicyDigest()) != 64 {
		t.Fatal("observable policy omitted from native receipt identity")
	}
}
