// Package influence handles normalized effects without language semantics.
package influence

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/Cyberlane/hayaku/internal/model"
)

func Validate(effect model.Effect) error {
	if effect.Owner == "" {
		return fmt.Errorf("effect has no owner")
	}
	switch effect.Kind {
	case "read", "directory", "exists", "metadata":
		if (effect.Path != "." && !fs.ValidPath(effect.Path)) || strings.ContainsAny(effect.Path, "\\:\x00") {
			return fmt.Errorf("effect input path is invalid")
		}
	case "unknown", "network", "subprocess", "clock", "random", "shared-state", "native":
		if effect.Path != "" {
			return fmt.Errorf("unsupported effect cannot assert a bounded path")
		}
	default:
		return fmt.Errorf("unknown effect kind")
	}
	return nil
}

func Unbounded(effect model.Effect) bool {
	return effect.Path == ""
}

// Affected includes directory membership and existence/metadata descendants.
// An unbounded effect always broadens, including when there are no Git changes.
func Affected(effect model.Effect, changed string) bool {
	if Unbounded(effect) {
		return true
	}
	if changed == "" {
		return false
	}
	if effect.Path == changed {
		return true
	}
	if effect.Kind == "directory" || effect.Kind == "exists" || effect.Kind == "metadata" {
		return effect.Path == "." || strings.HasPrefix(changed, effect.Path+"/") || strings.HasPrefix(effect.Path, changed+"/")
	}
	return false
}
