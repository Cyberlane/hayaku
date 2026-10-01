package vitest

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed trace.mjs
var traceHook []byte

// TraceHook returns an observational worker setup module. Hooks are advisory:
// successful transport does not establish complete runtime influence.
func TraceHook() []byte { return append([]byte(nil), traceHook...) }

// ValidateTrace applies the same transport checks to an untrusted persisted
// observation report. Gaps and valid observations remain advisory.
func ValidateTrace(trace Trace) error {
	if trace.Schema != 1 || !trace.Complete || len(trace.Owners) == 0 || len(trace.Owners)+len(trace.Observations)+len(trace.Gaps) > 100000 {
		return errors.New("invalid persisted runtime observations")
	}
	var records bytes.Buffer
	write := func(value any) error { return json.NewEncoder(&records).Encode(value) }
	for _, owner := range trace.Owners {
		if owner != "*" && !tracePath(owner, false) {
			return errors.New("invalid persisted runtime owner")
		}
		if err := write(map[string]any{"schema": 1, "type": "setup", "owner": owner}); err != nil {
			return err
		}
	}
	for _, observation := range trace.Observations {
		if len(observation.Path) > 16384 || len(observation.Owner) > 16384 || len(observation.Operation) > 32 {
			return errors.New("oversized persisted runtime observation")
		}
		if err := write(map[string]any{"schema": 1, "type": "observation", "owner": observation.Owner, "path": observation.Path, "operation": observation.Operation}); err != nil {
			return err
		}
	}
	for _, gap := range trace.Gaps {
		if !validTraceGap(gap) {
			return errors.New("invalid persisted runtime gap")
		}
		if err := write(map[string]any{"schema": 1, "type": "gap", "owner": trace.Owners[0], "code": gap}); err != nil {
			return err
		}
	}
	root, err := filepath.Abs(".")
	if err != nil {
		return err
	}
	_, err = DecodeTrace(records.Bytes(), root)
	return err
}

type Observation struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Owner     string `json:"owner"`
}

type Trace struct {
	Schema       int           `json:"schema"`
	Complete     bool          `json:"complete"`
	Owners       []string      `json:"owners"`
	Observations []Observation `json:"observations"`
	Gaps         []string      `json:"gaps"`
}

// DecodeTrace validates the bounded JSONL transport. Complete means a setup
// header was received, never that interception covers every runtime input.
func DecodeTrace(data []byte, root string) (Trace, error) {
	result := Trace{Schema: 1, Owners: []string{}, Observations: []Observation{}, Gaps: []string{}}
	if !filepath.IsAbs(root) || len(data) == 0 || len(data) > 32<<20 || data[len(data)-1] != '\n' {
		return result, errors.New("invalid runtime observation transport")
	}
	owners, observations, gaps, gapOwners := map[string]bool{}, map[Observation]bool{}, map[string]bool{}, map[string]bool{}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) > 100000 {
		return result, errors.New("runtime observation record limit")
	}
	for _, line := range lines {
		if len(line) == 0 || len(line) > 32768 {
			return result, errors.New("invalid runtime observation record size")
		}
		record, err := traceRecord(line)
		if err != nil {
			return result, err
		}
		owner, ok := record["owner"].(string)
		if !ok || owner != "*" && !tracePath(owner, false) || record["schema"] != json.Number("1") {
			return result, errors.New("invalid runtime observation identity")
		}
		switch record["type"] {
		case "setup":
			if len(record) != 3 {
				return result, errors.New("unexpected runtime setup fields")
			}
			owners[owner] = true
		case "observation":
			operation, operationOK := record["operation"].(string)
			file, fileOK := record["path"].(string)
			validOperation := operation == "read" || operation == "metadata" || operation == "exists" || operation == "directory" || operation == "readlink"
			if len(record) != 5 || !operationOK || !fileOK || !validOperation || !tracePath(file, operation != "read" && operation != "readlink") {
				return result, errors.New("invalid runtime observation")
			}
			observations[Observation{operation, file, owner}] = true
		case "gap":
			code, codeOK := record["code"].(string)
			if len(record) != 4 || !codeOK || !validTraceGap(code) {
				return result, errors.New("invalid runtime observation gap")
			}
			gaps[code] = true
			gapOwners[owner] = true
		default:
			return result, errors.New("unsupported runtime observation record")
		}
	}
	if len(owners) == 0 {
		return result, errors.New("missing runtime observation setup")
	}
	for owner := range gapOwners {
		if !owners[owner] && !owners["*"] {
			return result, errors.New("runtime gap without worker setup")
		}
	}
	for observation := range observations {
		if !owners[observation.Owner] && !owners["*"] {
			return result, errors.New("runtime observation without worker setup")
		}
		result.Observations = append(result.Observations, observation)
	}
	for owner := range owners {
		result.Owners = append(result.Owners, owner)
	}
	for gap := range gaps {
		result.Gaps = append(result.Gaps, gap)
	}
	sort.Strings(result.Owners)
	sort.Strings(result.Gaps)
	sort.Slice(result.Observations, func(i, j int) bool {
		a, b := result.Observations[i], result.Observations[j]
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Operation < b.Operation
	})
	result.Complete = true
	return result, nil
}

func traceRecord(line []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("invalid runtime observation JSON")
	}
	record := map[string]any{}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok {
			return nil, errors.New("invalid runtime observation key")
		}
		if _, exists := record[name]; exists {
			return nil, errors.New("duplicate runtime observation key")
		}
		value, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid runtime observation value")
		}
		switch value.(type) {
		case string, json.Number:
		default:
			return nil, errors.New("unsupported runtime observation value")
		}
		record[name] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, errors.New("invalid runtime observation ending")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing runtime observation JSON")
	}
	return record, nil
}

func tracePath(file string, rootAllowed bool) bool {
	return file != "" && len(file) <= 16384 && !strings.ContainsAny(file, "\x00\\\r\n") && !path.IsAbs(file) && path.Clean(file) == file && file != ".." && !strings.HasPrefix(file, "../") && (file != "." || rootAllowed) && !strings.Contains(file, ":")
}

func validTraceGap(code string) bool {
	switch code {
	case "outside-snapshot", "unsupported-path", "file-descriptor", "subprocess", "network", "native-addon", "worker", "filesystem-write", "record-limit", "unattributed-worker", "path-resolution", "clock", "random":
		return true
	}
	return false
}
