package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/capsule"
	"github.com/Cyberlane/hayaku/internal/suite"
	"github.com/Cyberlane/hayaku/internal/testutil"
)

func TestSuiteCLISelectiveReuseAndNativeProofRejection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows receipt ACL backend unqualified")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "suite.wasm"), testutil.WASIExit(0), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "billing.json"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	configuration := suiteConfig{Schema: 1, Groups: []capsuleConfig{
		{Schema: 1, Name: "billing", Module: "suite.wasm", Inputs: []string{"billing.json"}, Args: []string{"suite.wasm"}, ProducerIdentity: strings.Repeat("a", 64)},
		{Schema: 1, Name: "formatting", Module: "suite.wasm", Args: []string{"suite.wasm"}, ProducerIdentity: strings.Repeat("b", 64)},
	}}
	data, _ := json.Marshal(configuration)
	if err := os.WriteFile(filepath.Join(root, "suite.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	invoke := func() suite.Result {
		t.Helper()
		var out, errout bytes.Buffer
		if status := Run(context.Background(), []string{"suite", "--root", root, "--config", "suite.json", "--cache-dir", "receipts"}, &out, &errout); status != 0 {
			t.Fatalf("suite CLI failed: %s", errout.String())
		}
		var result suite.Result
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !result.Passed || !result.Complete {
			t.Fatal("successful CLI incomplete")
		}
		return result
	}
	if result := invoke(); result.Executed != 2 || result.Reused != 0 {
		t.Fatal("cold suite did not execute")
	}
	if result := invoke(); result.Executed != 0 || result.Reused != 2 {
		t.Fatal("warm suite did not reuse")
	}
	if err := os.WriteFile(filepath.Join(root, "billing.json"), []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := invoke(); result.Executed != 1 || result.Reused != 1 {
		t.Fatalf("changed group not isolated: %+v", result)
	}
	if err := os.WriteFile(filepath.Join(root, "suite.json"), []byte(`{"schema":1,"groups":[],"qualified":true,"backend":"native"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	if Run(context.Background(), []string{"suite", "--root", root, "--config", "suite.json"}, &out, &errout) == 0 {
		t.Fatal("native qualification declaration accepted")
	}
}

func TestSharedCapsuleLoaderRevalidatesInputsAndModules(t *testing.T) {
	root := t.TempDir()
	c := capsuleConfig{Schema: 1, Name: "test", Module: "suite.wasm", Inputs: []string{"input"}, Args: []string{"suite.wasm"}, ProducerIdentity: strings.Repeat("a", 64), Limits: capsule.DefaultLimits()}
	for _, name := range []string{"module", "input"} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, "suite.wasm"), testutil.WASIExit(0), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "input"), []byte("one"), 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := loadCapsule(context.Background(), root, c)
			if err != nil {
				t.Fatal(err)
			}
			if name == "module" {
				err = os.WriteFile(filepath.Join(root, "suite.wasm"), testutil.WASIExit(1), 0600)
			} else {
				err = os.WriteFile(filepath.Join(root, "input"), []byte("two"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if loaded.verify(context.Background(), root) == nil {
				t.Fatal("live input drift was accepted")
			}
		})
	}
}
