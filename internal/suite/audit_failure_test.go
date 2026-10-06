package suite

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Cyberlane/hayaku/internal/capsule"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/testutil"
)

func TestAuditFailureRevokesWholeActuallyExecutedSuiteDomain(t *testing.T) {
	for _, role := range []string{"local", "portable-verifier"} {
		for _, fault := range []string{"empty", "duplicate", "invalid-later-group", "too-many", "aggregate-limit", "canceled"} {
			t.Run(role+"/"+fault, func(t *testing.T) {
				var issuer, verifier *passcache.Authority
				if role == "portable-verifier" {
					public, private, err := ed25519.GenerateKey(rand.Reader)
					if err != nil {
						t.Fatal(err)
					}
					p := passcache.Policy{Schema: 1, Namespace: "independent-suite-audit", Epoch: 1, PublicKeys: []string{hex.EncodeToString(public)}}
					issuer, err = passcache.NewAuthority(p, private.Seed())
					if err != nil {
						t.Fatal(err)
					}
					verifier, err = passcache.NewAuthority(p, nil)
					if err != nil {
						t.Fatal(err)
					}
				}
				inventory := groups()
				cache := filepath.Join(t.TempDir(), "passes")
				options := Options{CacheDir: cache, Authority: issuer}
				cold, err := Run(context.Background(), inventory, options)
				if err != nil || !cold.Complete || !cold.Passed || cold.Executed != 2 || cold.Reused != 0 {
					t.Fatal("actual two-group healthy execution failed", err)
				}
				options.Authority = verifier
				warm, err := Run(context.Background(), inventory, options)
				if err != nil || warm.Reused != 2 || warm.Executed != 0 || !warm.Passed {
					t.Fatal("actual authenticated two-group warm reuse failed", err)
				}
				foreignKey := strings.Repeat("e", 64)
				foreign, err := passcache.OpenWithAuthority(cache, "independent-suite-foreign", issuer)
				if err != nil {
					t.Fatal(err)
				}
				defer foreign.Close()
				if err := foreign.Store(context.Background(), foreignKey, true); err != nil {
					t.Fatal(err)
				}
				foreignPath := filepath.Join(cache, foreignKey+".json")
				before, err := os.ReadFile(foreignPath)
				if err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				switch fault {
				case "empty":
					inventory = nil
				case "duplicate":
					inventory[1].Request.Name = inventory[0].Request.Name
				case "invalid-later-group":
					inventory[1].Request.Files = []capsule.File{{Path: "../outside", Data: []byte("untrusted")}}
				case "too-many":
					inventory = make([]Group, MaxGroups+1)
				case "aggregate-limit":
					// Share the bounded source slice: the actual aggregate, not
					// a fabricated counter, exceeds the 512 MiB admission bound.
					module := make([]byte, capsule.MaxModuleBytes)
					copy(module, inventory[0].Request.Module)
					inventory = make([]Group, 9)
					for i := range inventory {
						inventory[i].Request = capsule.Request{Name: strings.Repeat("a", i+1), Module: module, Args: []string{"suite.wasm"}, ProducerIdentity: strings.Repeat("a", 64)}
					}
				case "canceled":
					canceled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = canceled
				}
				options.NoReuse = true
				failed, err := Run(ctx, inventory, options)
				if err == nil || failed.Complete || failed.Passed || len(failed.Groups) != 0 {
					t.Fatal("invalid inventory executed or passed")
				}
				if fault == "canceled" && !errors.Is(err, context.Canceled) {
					t.Fatal("suite audit lost typed cancellation", err)
				}
				if fault == "aggregate-limit" && !strings.Contains(err.Error(), "suite input envelope exceeds bound") {
					t.Fatal("aggregate bound oracle masked by unrelated validation", err)
				}
				for _, group := range cold.Groups {
					if _, err := os.Stat(filepath.Join(cache, group.Key+".json")); !os.IsNotExist(err) {
						t.Fatal("failed aggregate audit retained actual group receipt", err)
					}
				}
				after, err := os.ReadFile(foreignPath)
				if err != nil || string(after) != string(before) {
					t.Fatal("aggregate audit mutated unrelated authenticated domain", err)
				}
				if _, status := foreign.Load(foreignKey); status != "hit" {
					t.Fatal("foreign receipt authentication lost", status)
				}
			})
		}
	}
}

func TestAuditFailureAbsentSuiteCacheCreatesNoAuthority(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "absent")
	result, err := Run(context.Background(), nil, Options{CacheDir: cache, NoReuse: true})
	if err == nil || result.Passed || result.Complete {
		t.Fatal("empty audited suite passed")
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("invalid suite created cache authority", err)
	}
}

func TestAuditFailureFreshSuiteRemovesEarlierActuallyStoredPass(t *testing.T) {
	for _, initial := range []string{"absent", "empty"} {
		t.Run(initial, func(t *testing.T) {
			cache := filepath.Join(t.TempDir(), "passes")
			if initial == "empty" {
				if err := os.Mkdir(cache, 0700); err != nil {
					t.Fatal(err)
				}
			}
			inventory := groups()
			inventory[1].Request.Module = testutil.WASIExit(1)
			result, err := Run(context.Background(), inventory, Options{CacheDir: cache, NoReuse: true})
			if err != nil || !result.Complete || result.Passed || len(result.Groups) != 2 || !result.Groups[0].Passed || result.Groups[0].CacheStatus != "stored" || result.Groups[0].Mode != "executed" || result.Groups[1].ExitCode != 1 {
				t.Fatal("actual first stored pass and later native failure not reached", err)
			}
			if _, err := os.Stat(filepath.Join(cache, result.Groups[0].Key+".json")); !os.IsNotExist(err) {
				t.Fatal("fresh failed aggregate retained earlier newly issued pass", err)
			}
		})
	}
}

// The fault is triggered by a real signed pass appearing, independently of
// implementation call counts. Renaming does not destroy its open root handle.
type auditFailureRetargetContext struct {
	context.Context
	cache, retained, replacement string
	once                         sync.Once
	mu                           sync.Mutex
	fault                        error
	observed                     bool
}

func (c *auditFailureRetargetContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, _ := os.ReadDir(c.cache)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			c.once.Do(func() {
				c.observed = true
				c.fault = os.Rename(c.cache, c.retained)
				if c.fault == nil {
					c.fault = os.Rename(c.replacement, c.cache)
				}
			})
			break
		}
	}
	if c.observed {
		return context.Canceled
	}
	return c.Context.Err()
}

func TestAuditFailureFreshSuiteRetainsCreatedRootAcrossRetarget(t *testing.T) {
	owner := t.TempDir()
	cache, retained, replacement := filepath.Join(owner, "passes"), filepath.Join(owner, "retained"), filepath.Join(owner, "replacement")
	foreignKey := strings.Repeat("d", 64)
	foreign, err := passcache.Open(replacement, "independent-replacement-provider")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if err := foreign.Store(context.Background(), foreignKey, true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(replacement, foreignKey+".json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := &auditFailureRetargetContext{Context: context.Background(), cache: cache, retained: retained, replacement: replacement}
	result, err := Run(ctx, groups(), Options{CacheDir: cache, NoReuse: true})
	if !ctx.observed || ctx.fault != nil {
		t.Fatal("actual newly stored pass retarget milestone not reached", ctx.fault)
	}
	if !errors.Is(err, context.Canceled) || result.Passed || result.Complete {
		t.Fatal("retargeted canceled suite lost original typed failure", err)
	}
	entries, err := os.ReadDir(retained)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			t.Fatal("audit reopened replacement path and left original signed pass authorized")
		}
	}
	after, err := os.ReadFile(filepath.Join(cache, foreignKey+".json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("cleanup mutated replacement foreign receipt", err)
	}
	if _, status := foreign.Load(foreignKey); status != "hit" {
		t.Fatal("replacement foreign authority no longer valid", status)
	}
}
