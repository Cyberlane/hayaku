package native

import (
	"slices"

	"github.com/Cyberlane/hayaku/internal/model"
)

// Inventories are producer-owned evidence. Never expose the backing slices of
// a seal to a decoder, receipt validator or caller. BindingDigest includes the
// complete inventory, so changing identities invalidates the producer seal.
func cloneInventory(inventory model.ExecutionInventory) model.ExecutionInventory {
	inventory.Units = slices.Clone(inventory.Units)
	inventory.Cases = slices.Clone(inventory.Cases)
	return inventory
}
