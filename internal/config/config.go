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
			if w.Adapter != "vitest" && w.Adapter != "node-test" && w.Adapter != "jest" && w.Adapter != "playwright" {
				return errors.New("node_runtime requires a supported native Node runner")
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
