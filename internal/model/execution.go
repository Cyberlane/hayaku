package model

// ExecutionSchema versions closed execution evidence independently of plans.
const ExecutionSchema = 1

// Case is an independently collected identity, including its owning unit.
// Identically named cases in different units remain distinct.
type Case struct {
	Unit string `json:"unit"`
	ID   string `json:"id"`
}

// ExecutionInventory is data, not qualification authority. Providers must
// establish completeness inside their reviewed collection/runtime contract.
type ExecutionInventory struct {
	Schema int    `json:"schema"`
	Units  []Unit `json:"units"`
	Cases  []Case `json:"cases"`
}

// ExecutionBinding records the identities of one closed execution group.
// Digests bind complete input/effect and original-scope context, not observations
// of which inputs happened to be accessed. Shape validation cannot prove them.
// No declaration here can replace an in-process provider/backend qualification.
type ExecutionBinding struct {
	Schema        int                `json:"schema"`
	Group         string             `json:"group"`
	ProgramDigest string             `json:"program_digest"`
	InputsDigest  string             `json:"inputs_digest"`
	ProofDigest   string             `json:"proof_digest"`
	PolicyDigest  string             `json:"policy_digest"`
	ContextDigest string             `json:"context_digest"`
	Inventory     ExecutionInventory `json:"inventory"`
}
