package native

import (
	"errors"
	"strings"

	"github.com/Cyberlane/hayaku/internal/graph"
	"github.com/Cyberlane/hayaku/internal/influence"
	"github.com/Cyberlane/hayaku/internal/model"
)

// Native runtime clocks, entropy and metadata are not virtualized. The reviewed
// provider must establish that user-visible outcomes cannot depend on them.
// Kernel containment, traces and an empty declared summary cannot do so.
const observablePolicy = "native-user-observables-reject-v1"

func observablePolicyDigest() string {
	return identity([]string{contractVersion, observablePolicy, "metadata", "clock", "random", "unknown"})
}

// validateObservableEffects is an additional veto, never a provider proof. It
// validates descriptive summaries only after independent complete qualification.
// Other capabilities retain their separate input/runtime/provider obligations.
func validateObservableEffects(effects []model.Effect) error {
	if len(effects) > graph.MaxEdges {
		return errors.New("native observable effect summary exceeds bounds")
	}
	remaining := 16 << 20
	for _, effect := range effects {
		if len(effect.Owner) > 8192 || len(effect.Kind) > 64 || len(effect.Path) > 4096 {
			return errors.New("native observable effect identity exceeds bounds")
		}
		bytes := len(effect.Owner) + len(effect.Kind) + len(effect.Path)
		if bytes > remaining {
			return errors.New("native observable effect summary exceeds bounds")
		}
		remaining -= bytes
		if validateNativeArguments([]string{effect.Owner, effect.Kind, effect.Path}, nil) != nil || strings.ContainsAny(effect.Owner, "\r\n") || strings.ContainsAny(effect.Path, "\r\n") {
			return errors.New("invalid native observable effect identity")
		}
		if influence.Validate(effect) != nil {
			return errors.New("invalid native observable effect shape")
		}
		switch effect.Kind {
		case "metadata", "clock", "random", "unknown":
			return errors.New("native observable effects are unqualified")
		}
	}
	return nil
}
