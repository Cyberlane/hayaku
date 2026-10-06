// Package config validates explicit execution contexts and workspace policy.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/model"
)

const MaxBytes = 1 << 20

func Decode(r io.Reader) (model.Config, error) {
	var c model.Config
	if err := DecodeValue(r, &c, MaxBytes); err != nil {
		return c, err
	}
	return c, Validate(c)
}

// DecodeValue applies the same duplicate-key, size, unknown-field and trailing
// data policy to auxiliary configuration and measurement documents.
func DecodeValue(r io.Reader, value any, maxBytes int64) error {
	if maxBytes < 1 || maxBytes > 64<<20 {
		return errors.New("invalid JSON document limit")
	}
	b, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return err
	}
	if int64(len(b)) > maxBytes {
		return errors.New("configuration exceeds size limit")
	}
	// Duplicate keys are rejected rather than silently allowing policy replacement.
	if err := uniqueKeys(json.NewDecoder(bytes.NewReader(b))); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("configuration contains trailing JSON")
	}
	return nil
}

func uniqueKeys(d *json.Decoder) error {
	return uniqueKeysDepth(d, 0)
}

func uniqueKeysDepth(d *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("configuration nesting exceeds limit")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return errors.New("unexpected JSON delimiter")
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("invalid or duplicate configuration key")
			}
			seen[name] = true
		}
		if err := uniqueKeysDepth(d, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func Validate(c model.Config) error {
	if c.Schema != model.Schema {
		return fmt.Errorf("unsupported config schema %d", c.Schema)
	}
	if c.Context.ID == "" || c.Context.OS == "" || c.Context.Arch == "" {
		return errors.New("context id, os and arch are required")
	}
	if len(c.Workspaces) == 0 {
		return errors.New("at least one workspace is required")
	}
	if err := ValidateProducerStages(c.Producers); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, w := range c.Workspaces {
		if w.ID == "" || strings.ContainsAny(w.ID, ":\x00\n") || seen[w.ID] {
			return errors.New("workspace IDs must be unique and nonempty without colons")
		}
		seen[w.ID] = true
		if !Relative(w.Root) {
			return fmt.Errorf("workspace %s root must remain in repository", w.ID)
		}
		if w.Adapter == "" {
			return fmt.Errorf("workspace %s requires an adapter", w.ID)
		}
		if w.NodeRuntime != nil {
			if w.Adapter != "vitest" && w.Adapter != "node-test" && w.Adapter != "jest" && w.Adapter != "playwright" && w.Adapter != "command" {
				return errors.New("node_runtime requires a supported Node runner or full command")
			}
			if len(w.Prerequisites) != 0 {
				return errors.New("bound native Node runtime does not support prerequisites or generated source inputs; use an independently captured envelope")
			}
			for _, path := range []string{w.NodeRuntime.Node, w.NodeRuntime.Modules} {
				if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\n") {
					return errors.New("node_runtime requires clean absolute node and modules paths")
				}
			}
			if filepath.Base(w.NodeRuntime.Modules) != "node_modules" {
				return errors.New("node_runtime modules must name a node_modules directory")
			}
		}
		if w.SwiftRuntime != nil {
			if w.Adapter != "swift" || !filepath.IsAbs(w.SwiftRuntime.Dependencies) || filepath.Clean(w.SwiftRuntime.Dependencies) != w.SwiftRuntime.Dependencies || strings.ContainsAny(w.SwiftRuntime.Dependencies, "\x00\n") {
				return errors.New("swift_runtime requires a Swift workspace and a clean absolute provisioned dependency path")
			}
		}
		commands := append([]model.Command{w.Command}, w.Prerequisites...)
		for _, cmd := range commands {
			if err := ValidateCommand(cmd); err != nil {
				return fmt.Errorf("workspace %s: %w", w.ID, err)
			}
		}
		for _, input := range w.Inputs {
			if !Relative(input) || input == "." {
				return fmt.Errorf("invalid workspace input %q", input)
			}
		}
	}
	for key, value := range c.Context.Env {
		if (key == "GOOS" && value != c.Context.OS) || (key == "GOARCH" && value != c.Context.Arch) {
			return errors.New("explicit Go target contradicts execution context")
		}
		if key == "PATH" && value != os.Getenv("PATH") {
			return errors.New("a different PATH override is unsupported; use absolute runner paths and the actual host PATH")
		}
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return errors.New("invalid context environment")
		}
	}
	for _, contract := range c.Contracts {
		if !Relative(contract.Input) || contract.Input == "." || len(contract.Consumers) == 0 || contract.Reason == "" {
			return errors.New("contracts require a repository input, consumers and reason")
		}
	}
	return nil
}

func Relative(p string) bool {
	return p != "" && !filepath.IsAbs(p) && filepath.ToSlash(filepath.Clean(p)) == p && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00")
}

func ValidateCommand(c model.Command) error {
	if !Relative(c.Dir) || c.Executable == "" || strings.ContainsAny(c.Executable, "\x00\n") {
		return errors.New("commands require relative cwd and a nonempty executable")
	}
	for _, arg := range c.Args {
		if strings.ContainsRune(arg, 0) {
			return errors.New("command arguments contain NUL")
		}
	}
	return nil
}

func Digest(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		panic(err)
	} // Only JSON model values are passed by callers.
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func HostContext() model.Context {
	return model.Context{ID: "local", OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// ValidateProducerStages applies the same bounded declaration policy to config
// decoding and direct producer APIs. Filesystem admission happens at capture.
func ValidateProducerStages(stages []model.ProducerStage) error {
	if len(stages) > 32 {
		return errors.New("producer stage count exceeds limit")
	}
	seen, mounts := map[string]bool{}, []string{}
	argumentBytes := 0
	for _, stage := range stages {
		validText := func(value string) bool { return utf8.ValidString(value) && !strings.ContainsRune(value, 0) }
		if len(stage.ID) == 0 || len(stage.ID) > 64 || seen[stage.ID] || len(stage.Output) > 4096 || len(stage.Command.Executable) > 4096 || len(stage.Command.Dir) > 4096 || len(stage.Command.Args) > 1024 || len(stage.DependsOn) > 32 {
			return errors.New("invalid producer stage bounds")
		}
		for _, r := range stage.ID {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return errors.New("invalid producer stage identity")
			}
		}
		if !validText(stage.Output) || !validText(stage.Command.Executable) || !validText(stage.Command.Dir) || !Relative(stage.Output) || stage.Output == "." || !filepath.IsAbs(stage.Command.Executable) || filepath.Clean(stage.Command.Executable) != stage.Command.Executable || ValidateCommand(stage.Command) != nil {
			return errors.New("invalid producer stage command or output")
		}
		for _, argument := range stage.Command.Args {
			argumentBytes += len(argument)
			if !validText(argument) || len(argument) > 32768 || argumentBytes > 1<<20 {
				return errors.New("producer arguments exceed size limit")
			}
		}
		dependencies := map[string]bool{}
		for _, dependency := range stage.DependsOn {
			if !seen[dependency] || dependencies[dependency] {
				return errors.New("producer dependencies must name distinct earlier stages")
			}
			dependencies[dependency] = true
		}
		for _, previous := range mounts {
			if previous == stage.Output || strings.HasPrefix(previous, stage.Output+"/") || strings.HasPrefix(stage.Output, previous+"/") {
				return errors.New("producer output trees overlap")
			}
		}
		seen[stage.ID] = true
		mounts = append(mounts, stage.Output)
	}
	return nil
}
