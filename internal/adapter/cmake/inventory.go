package cmake

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/runner"
	native "github.com/Cyberlane/hayaku/internal/runner/native"
)

type Inventory struct {
	Runner, ContextDigest string
	Execution             model.ExecutionInventory
	Gaps                  []model.Gap
}

func binaryInventory(id, framework string, names []string, context any) (Inventory, error) {
	r := Inventory{Runner: framework, ContextDigest: digest(context), Execution: model.ExecutionInventory{Schema: model.ExecutionSchema, Units: []model.Unit{{ID: id, Workspace: "cmake", Selector: id, Kind: framework + "-binary"}}}, Gaps: []model.Gap{{Code: "cmake-inventory-unqualified", Detail: "Native registration does not establish runtime influence or independent case execution."}}}
	for _, name := range names {
		r.Execution.Cases = append(r.Execution.Cases, model.Case{Unit: id, ID: name})
	}
	if err := runner.ValidateInventory(r.Execution); err != nil {
		return Inventory{}, err
	}
	return r, nil
}

func googleNames(data []byte) ([]string, error) {
	if len(data) > native.MaxResultBytes {
		return nil, errors.New("GoogleTest list exceeds bound")
	}
	var suite string
	var names []string
	banner := false
	suiteCases := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if suite == "" && !banner && strings.HasPrefix(line, "Running main() from ") && identity(line) {
			banner = true
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			if suite != "" && suiteCases == 0 {
				return nil, errors.New("GoogleTest suite has no listed cases")
			}
			value, _, _ := strings.Cut(line, " #")
			value = strings.TrimRight(value, " ")
			if !identity(value) || !strings.HasSuffix(value, ".") || strings.ContainsAny(value, " \t") {
				return nil, errors.New("unsupported GoogleTest suite listing")
			}
			suite = value
			suiteCases = 0
			continue
		}
		value, _, _ := strings.Cut(strings.TrimPrefix(line, "  "), " #")
		value = strings.TrimRight(value, " ")
		if suite == "" || !identity(value) || strings.ContainsAny(value, " \t") || len(names) >= 100000 {
			return nil, errors.New("unsupported GoogleTest case listing")
		}
		names = append(names, suite+value)
		suiteCases++
	}
	if suite != "" && suiteCases == 0 {
		return nil, errors.New("GoogleTest final suite listing is incomplete")
	}
	return names, nil
}

// GoogleTest lists native registered identities, including typed/value suffixes.
// Disabled and filtered cases retain their native names; no case-splitting or
// command-equivalence claim follows from this list.
func (c *Closure) GoogleTest(ctx context.Context, id string, args []string) (Inventory, error) {
	if c == nil || c.session == nil {
		return Inventory{}, errors.New("missing CMake closure")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := bounded(args, 1, 4096); err != nil {
		return Inventory{}, err
	}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--gtest_filter=") || !identity(arg) {
			return Inventory{}, errors.New("unsupported GoogleTest collection option")
		}
	}
	exe, err := c.executable(ctx, id)
	if err != nil {
		return Inventory{}, err
	}
	argv := append(append([]string(nil), args...), "--gtest_list_tests")
	out, err := c.session.Run(ctx, model.Command{Executable: exe, Dir: "{build}", Args: argv})
	if err != nil || !out.Completed || out.ExitCode != 0 {
		return Inventory{}, errors.Join(err, errors.New("GoogleTest enumeration failed"))
	}
	names, err := googleNames(out.Stdout)
	if err != nil {
		return Inventory{}, err
	}
	r, err := binaryInventory(id, "gtest", names, struct {
		API            string
		Args           []string
		Stdout, Stderr []byte
	}{c.api.Digest, argv, out.Stdout, out.Stderr})
	return c.bindInventory(ctx, r, err)
}

func catchNames(data []byte) ([]string, error) {
	if len(data) > native.MaxResultBytes {
		return nil, errors.New("Catch2 list exceeds bound")
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	var stack []string
	var names []string
	rootClosed, caseName := false, false
	var name strings.Builder
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			if token.Name.Space != "" || rootClosed || len(stack) >= 32 {
				return nil, errors.New("unsupported Catch2 XML structure")
			}
			if len(stack) == 3 && stack[2] == "Name" {
				return nil, errors.New("nested Catch2 case name")
			}
			if len(stack) == 0 && token.Name.Local != "MatchingTests" {
				return nil, errors.New("unsupported Catch2 listing root")
			}
			stack = append(stack, token.Name.Local)
			if len(stack) == 2 {
				if token.Name.Local != "TestCase" {
					return nil, errors.New("unexpected Catch2 list item")
				}
				caseName = false
			}
			if len(stack) == 3 && token.Name.Local == "Name" {
				if caseName || len(names) >= 100000 {
					return nil, errors.New("duplicate or excessive Catch2 case names")
				}
				name.Reset()
			}
		case xml.EndElement:
			if len(stack) == 3 && stack[2] == "Name" {
				if !identity(name.String()) {
					return nil, errors.New("invalid Catch2 registered name")
				}
				names = append(names, name.String())
				caseName = true
			}
			if len(stack) == 2 && !caseName {
				return nil, errors.New("Catch2 case has no name")
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				rootClosed = true
			}
		case xml.CharData:
			if len(stack) == 3 && stack[2] == "Name" {
				if name.Len()+len(token) > 4096 {
					return nil, errors.New("Catch2 case name exceeds bound")
				}
				name.Write(token)
			}
			if len(stack) == 0 && strings.TrimSpace(string(token)) != "" {
				return nil, errors.New("trailing Catch2 data")
			}
		case xml.Directive:
			return nil, errors.New("Catch2 XML directive is not admitted")
		}
	}
	if !rootClosed || len(stack) != 0 {
		return nil, errors.New("incomplete Catch2 list")
	}
	return names, nil
}

// Catch2 enumerates registered cases for the explicit spec/order/seed context.
// Sections and generator paths remain part of the owning binary lifecycle.
func (c *Closure) Catch2(ctx context.Context, id string, specs []string, order, seed string) (Inventory, error) {
	if c == nil || c.session == nil {
		return Inventory{}, errors.New("missing CMake closure")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := bounded(specs, 1015, 4096); err != nil {
		return Inventory{}, err
	}
	if order != "decl" && order != "lex" && order != "rand" {
		return Inventory{}, errors.New("unsupported Catch2 order")
	}
	if len(seed) == 0 || len(seed) > 10 || strings.Trim(seed, "0123456789") != "" {
		return Inventory{}, errors.New("explicit Catch2 integer seed required")
	}
	for _, spec := range specs {
		if !identity(spec) || strings.HasPrefix(spec, "-") {
			return Inventory{}, errors.New("invalid Catch2 test spec")
		}
	}
	exe, err := c.executable(ctx, id)
	if err != nil {
		return Inventory{}, err
	}
	args := []string{"--list-tests", "--reporter", "xml", "--order", order, "--rng-seed", seed}
	args = append(args, specs...)
	out, err := c.session.Run(ctx, model.Command{Executable: exe, Dir: "{build}", Args: args})
	if err != nil || !out.Completed || out.ExitCode != 0 {
		return Inventory{}, errors.Join(err, errors.New("Catch2 enumeration failed"))
	}
	names, err := catchNames(out.Stdout)
	if err != nil {
		return Inventory{}, err
	}
	r, err := binaryInventory(id, "catch2", names, struct {
		API            string
		Args           []string
		Stdout, Stderr []byte
	}{c.api.Digest, args, out.Stdout, out.Stderr})
	if err == nil {
		r.Gaps = append(r.Gaps, model.Gap{Code: "catch2-sections-generators", Detail: "Registered case listing does not enumerate dynamic section or generator paths."})
	}
	return c.bindInventory(ctx, r, err)
}

type ctestList struct {
	Kind    string
	Version struct{ Major, Minor int }
	Tests   []struct {
		Name       string
		Command    []string
		Properties []struct {
			Name  string
			Value json.RawMessage
		}
	}
}

func ctestInventory(data []byte, args []string) (Inventory, error) {
	var list ctestList
	if err := decode(data, &list); err != nil {
		return Inventory{}, err
	}
	if list.Kind != "ctestInfo" || list.Version.Major != 1 || list.Version.Minor != 0 || len(list.Tests) == 0 || len(list.Tests) > 50000 {
		return Inventory{}, errors.New("unsupported CTest inventory")
	}
	r := Inventory{Runner: "ctest", ContextDigest: digest(struct {
		Data []byte
		Args []string
	}{data, args}), Execution: model.ExecutionInventory{Schema: model.ExecutionSchema}, Gaps: []model.Gap{{Code: "ctest-lifecycle-unqualified", Detail: "CTest properties and executable scope retain fixtures, environment, working directory, resource and failure semantics; native influence remains unqualified."}}}
	for _, test := range list.Tests {
		if !identity(test.Name) || len(test.Command) > 1024 || len(test.Properties) > 4096 {
			return Inventory{}, errors.New("invalid CTest test")
		}
		for _, arg := range test.Command {
			if !identity(arg) {
				return Inventory{}, errors.New("invalid CTest command")
			}
		}
		seen := map[string]bool{}
		for _, prop := range test.Properties {
			if !identity(prop.Name) || seen[prop.Name] || len(prop.Value) == 0 {
				return Inventory{}, errors.New("invalid CTest property")
			}
			seen[prop.Name] = true
		}
		if len(test.Command) == 0 {
			r.Gaps = append(r.Gaps, model.Gap{Code: "ctest-command-unresolved", Detail: "A selected test has no resolved command; retain its original configured CTest invocation."})
		}
		id := "ctest:" + test.Name
		r.Execution.Units = append(r.Execution.Units, model.Unit{ID: id, Workspace: "cmake", Selector: test.Name, Kind: "ctest-executable"})
		r.Execution.Cases = append(r.Execution.Cases, model.Case{Unit: id, ID: test.Name})
	}
	if err := runner.ValidateInventory(r.Execution); err != nil {
		return Inventory{}, err
	}
	return r, nil
}

func (c *Closure) CTest(ctx context.Context, args []string) (Inventory, error) {
	if err := bounded(args, 1022, 4096); err != nil {
		return Inventory{}, err
	}
	if c == nil || c.session == nil || c.request.CTest == "" {
		return Inventory{}, errors.New("explicit installed CTest required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	allowed := map[string]bool{"-C": true, "-R": true, "-E": true, "-L": true, "-LE": true, "-FA": true, "-FS": true, "-FC": true, "--resource-spec-file": true}
	for i := 0; i < len(args); i += 2 {
		if !allowed[args[i]] || i+1 >= len(args) || !identity(args[i+1]) {
			return Inventory{}, errors.New("unsupported CTest collection scope")
		}
	}
	argv := append(append([]string(nil), args...), "--show-only=json-v1")
	records, _, err := c.session.Records(ctx)
	if err != nil {
		return Inventory{}, err
	}
	out, err := c.session.RunProducer(ctx, "ctest-list-"+strconv.Itoa(len(records)), model.Command{Executable: c.request.CTest, Args: argv, Dir: "{build}"})
	if err != nil || !out.Completed || out.ExitCode != 0 {
		return Inventory{}, errors.Join(err, errors.New("CTest enumeration failed"))
	}
	// The query owns a new build generation; stale graph data must not remain
	// available if its native metadata or refreshed graph is invalid.
	c.api = FileAPI{}
	r, err := ctestInventory(out.Stdout, args)
	if err != nil {
		return Inventory{}, err
	}
	fresh, err := CollectFileAPI(ctx, c.session)
	if err != nil {
		return Inventory{}, err
	}
	if err = admitCompilers(ctx, c.session, c.request, fresh); err != nil {
		return Inventory{}, err
	}
	fresh, err = observeDependencies(ctx, c.session, c.request.Ninja, fresh)
	if err != nil {
		return Inventory{}, err
	}
	c.api = fresh
	if err == nil {
		r.ContextDigest = digest([]string{c.api.Digest, r.ContextDigest})
	}
	return c.bindInventory(ctx, r, err)
}

func (c *Closure) bindInventory(ctx context.Context, inventory Inventory, err error) (Inventory, error) {
	if err != nil {
		return Inventory{}, err
	}
	_, binding, err := c.session.Records(ctx)
	if err != nil {
		return Inventory{}, err
	}
	inventory.ContextDigest = digest([]string{inventory.ContextDigest, binding})
	return inventory, nil
}
