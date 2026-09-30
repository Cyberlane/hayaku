package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

const valid = `{"schema":1,"context":{"id":"local","os":"darwin","arch":"arm64"},"workspaces":[{"id":"go","root":".","adapter":"go","command":{"cwd":".","executable":"go","argv":["test","./..."]}}]}`

func TestDecodeStrictPolicy(t *testing.T) {
	if _, err := Decode(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		strings.Replace(valid, `"schema":1`, `"schema":1,"schema":1`, 1),
		strings.Replace(valid, `"schema":1`, `"schema":1,"skip_anyway":true`, 1),
		strings.Replace(valid, `"root":"."`, `"root":"../outside"`, 1),
		valid + `{}`, `null`,
	} {
		if _, err := Decode(strings.NewReader(text)); err == nil {
			t.Fatalf("accepted malformed policy %s", text)
		}
	}
}

func TestNodeRuntimeConfigurationIsExplicitAndVitestOnly(t *testing.T) {
	c, err := Decode(strings.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	c.Workspaces[0].Adapter = "vitest"
	root := t.TempDir()
	c.Workspaces[0].NodeRuntime = &model.NodeRuntime{Node: filepath.Join(root, "node"), Modules: filepath.Join(root, "node_modules")}
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(strings.NewReader(string(encoded))); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*model.Config){
		func(c *model.Config) { c.Workspaces[0].Adapter = "go" },
		func(c *model.Config) { c.Workspaces[0].Prerequisites = []model.Command{c.Workspaces[0].Command} },
		func(c *model.Config) { c.Workspaces[0].NodeRuntime.Node = "node" },
		func(c *model.Config) { c.Workspaces[0].NodeRuntime.Modules = filepath.Join(root, "other") },
	} {
		copyConfig := c
		copyConfig.Workspaces = append([]model.Workspace{}, c.Workspaces...)
		runtime := *c.Workspaces[0].NodeRuntime
		copyConfig.Workspaces[0].NodeRuntime = &runtime
		change(&copyConfig)
		if err := Validate(copyConfig); err == nil {
			t.Fatal("accepted invalid runtime configuration")
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(valid)
	f.Add(`{"schema":1,"schema":2}`)
	f.Fuzz(func(t *testing.T, s string) {
		c, err := Decode(strings.NewReader(s))
		if err == nil {
			if err := Validate(c); err != nil {
				t.Fatal(err)
			}
			if Digest(c) == "" {
				t.Fatal("emptydigest")
			}
		}
	})
}
