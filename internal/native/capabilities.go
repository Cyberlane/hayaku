package native

import (
	"errors"
	"github.com/Cyberlane/hayaku/internal/model"
)

// Capability declarations never certify a complete subprocess, native extension
// or service boundary. Managed-child envelopes remain operational and receipt
// ineligible. This is only an extra veto after a complete in-process provider.
func validateCapabilityEffects(effects []model.Effect) error {
	if err := validateObservableEffects(effects); err != nil {
		return err
	}
	for _, effect := range effects {
		switch effect.Kind {
		case "subprocess", "native", "network", "shared-state":
			return errors.New("native subprocess, extension or service effects are unqualified")
		}
	}
	return nil
}
func capabilityPolicyDigest() string {
	return identity([]string{contractVersion, "native-capabilities-reject-v1", "native-fixed-static-child-v1", "subprocess", "native", "network", "shared-state"})
}
