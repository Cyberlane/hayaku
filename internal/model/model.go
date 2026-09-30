// Package model defines versioned, ecosystem-independent planning contracts.
package model

const Schema = 1

type Command struct {
	Dir        string   `json:"cwd"`
	Executable string   `json:"executable"`
	Args       []string `json:"argv"`
}

type Workspace struct {
	ID            string       `json:"id"`
	Root          string       `json:"root"`
	Adapter       string       `json:"adapter"`
	Command       Command      `json:"command"`
	Prerequisites []Command    `json:"prerequisites,omitempty"`
	Patterns      []string     `json:"patterns,omitempty"`
	BuildFlags    []string     `json:"build_flags,omitempty"`
	Inputs        []string     `json:"inputs,omitempty"`
	NodeRuntime   *NodeRuntime `json:"node_runtime,omitempty"`
}

// NodeRuntime declares preinstalled, content-bound runtime inputs. Hayaku never
// installs dependencies. Absolute paths remain in configuration, not reports.
type NodeRuntime struct {
	Node    string `json:"node"`
	Modules string `json:"modules"`
}

type Context struct {
	ID   string            `json:"id"`
	OS   string            `json:"os"`
	Arch string            `json:"arch"`
	Env  map[string]string `json:"env,omitempty"`
}

type Contract struct {
	Input     string   `json:"input"`
	Consumers []string `json:"consumers"`
	Reason    string   `json:"reason"`
}

type Config struct {
	Schema     int         `json:"schema"`
	Context    Context     `json:"context"`
	Workspaces []Workspace `json:"workspaces"`
	Contracts  []Contract  `json:"contracts,omitempty"`
}

type Change struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	Status  string `json:"status"`
}

// Edge points from an influence to its dependent, never the other way round.
type Edge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

type Unit struct {
	ID        string `json:"id"`
	Workspace string `json:"workspace"`
	Selector  string `json:"selector"`
	Kind      string `json:"kind"`
}

type Gap struct {
	Code      string `json:"code"`
	Workspace string `json:"workspace,omitempty"`
	Detail    string `json:"detail"`
}

type Evidence struct {
	Nodes     []string            `json:"nodes"`
	Adapter   string              `json:"adapter"`
	Version   string              `json:"version"`
	Workspace string              `json:"workspace"`
	Inputs    map[string][]string `json:"inputs"`
	Edges     []Edge              `json:"edges"`
	Units     []Unit              `json:"units"`
	Gaps      []Gap               `json:"gaps"`
}

type Reason struct {
	Unit  string   `json:"unit"`
	Code  string   `json:"code"`
	Input string   `json:"input,omitempty"`
	Via   []string `json:"via,omitempty"`
}

type Plan struct {
	ToolsDigest      string     `json:"tools_digest"`
	Schema           int        `json:"schema"`
	Version          string     `json:"version"`
	Base             string     `json:"base"`
	Candidate        string     `json:"candidate"`
	ConfigDigest     string     `json:"config_digest"`
	ContextDigest    string     `json:"context_digest"`
	Mode             string     `json:"mode"`
	Changes          []Change   `json:"changes"`
	Evidence         []Evidence `json:"evidence"`
	Proposed         []Unit     `json:"proposed_units"`
	Selected         []Unit     `json:"selected_units"`
	Reasons          []Reason   `json:"reasons"`
	Gaps             []Gap      `json:"gaps"`
	Commands         []Command  `json:"commands"`
	ProposalCommands []Command  `json:"proposal_commands"`
}
