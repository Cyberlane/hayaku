package influence

import (
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func TestResourceMembershipAndMissingPaths(t *testing.T) {
	for _, c := range []struct {
		kind, path, changed string
		want                bool
	}{
		{"read", "fixtures/value.json", "fixtures/value.json", true},
		{"read", "fixtures/value.json", "fixtures/other.json", false},
		{"directory", "fixtures", "fixtures/new.json", true},
		{"directory", "fixtures", "fixtures/sub/new.json", true},
		{"directory", "fixtures", "fixtures-old/value.json", false},
		{"directory", "fixtures/sub", "fixtures", true},
		{"exists", "optional.json", "optional.json", true},
		{"metadata", "fixtures", "fixtures/new.json", true},
		{"directory", ".", "any/file", true},
		{"unknown", "", "", true},
	} {
		e := model.Effect{Owner: "test", Kind: c.kind, Path: c.path}
		if err := Validate(e); err != nil {
			t.Fatal(err)
		}
		if got := Affected(e, c.changed); got != c.want {
			t.Errorf("%s %s changed=%s got=%t", c.kind, c.path, c.changed, got)
		}
	}
}

func TestMalformedEffectsReject(t *testing.T) {
	for _, e := range []model.Effect{
		{Kind: "read", Path: "file"}, {Owner: "test", Kind: "unsupported"},
		{Owner: "test", Kind: "read", Path: "../secret"}, {Owner: "test", Kind: "read", Path: "/secret"},
		{Owner: "test", Kind: "read", Path: "a\\b"}, {Owner: "test", Kind: "network", Path: "declared-safe"},
	} {
		if Validate(e) == nil {
			t.Errorf("malformed effect accepted: %+v", e)
		}
	}
}
