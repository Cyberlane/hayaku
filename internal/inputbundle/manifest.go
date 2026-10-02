package inputbundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
)

const MaxManifestBytes = 32 << 20

// DecodeManifest reads strict, bounded identity evidence. A saved manifest has
// no file bytes and confers no execution authority. Capture current inputs and
// compare the digest before materializing or using it.
func DecodeManifest(reader io.Reader) (Manifest, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxManifestBytes+1))
	if err != nil || len(data) > MaxManifestBytes {
		return Manifest{}, errors.New("input manifest exceeds bounds or cannot be read")
	}
	if err := uniqueKeys(data); err != nil {
		return Manifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, errors.New("invalid input manifest schema")
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// ValidateManifest rejects malformed inventories and verifies their canonical
// digest. Digests identify content; they do not authenticate a producer.
func ValidateManifest(manifest Manifest) error {
	if manifest.Schema != 1 || !validDigest(manifest.Digest) || len(manifest.Roots) == 0 || len(manifest.Roots) > 128 || len(manifest.Inputs) == 0 || len(manifest.Inputs) > MaxEntries || len(manifest.Entries) == 0 || len(manifest.Entries) > MaxEntries || manifest.Bytes < 0 || manifest.Bytes > MaxBytes {
		return errors.New("invalid input manifest bounds")
	}
	roots := map[string]Mount{}
	previous := ""
	for _, root := range manifest.Roots {
		if !validPath(root.Name, false) || strings.Contains(root.Name, "/") || root.Name <= previous || !validPath(root.Path, true) || root.Mode > 0777 {
			return errors.New("invalid input manifest root")
		}
		for _, other := range roots {
			if root.Path == other.Path || (root.Path != "." && other.Path != "." && (nested(root.Path, other.Path) || nested(other.Path, root.Path))) {
				return errors.New("input manifest mounts overlap")
			}
		}
		roots[root.Name] = root
		previous = root.Name
	}
	previousRoot, previousPath := "", ""
	for _, input := range manifest.Inputs {
		if _, ok := roots[input.Root]; !ok || !validPath(input.Path, true) || input.Root < previousRoot || (input.Root == previousRoot && input.Path <= previousPath) {
			return errors.New("invalid input manifest selection")
		}
		previousRoot, previousPath = input.Root, input.Path
	}
	entries := map[string]Entry{}
	previous = ""
	total := int64(0)
	for _, entry := range manifest.Entries {
		if !validPath(entry.Path, true) || entry.Path <= previous || entry.Mode > 0777 {
			return errors.New("invalid input manifest entry")
		}
		previous = entry.Path
		switch entry.Kind {
		case "directory":
			if entry.Size != 0 || entry.SHA256 != "" || entry.Link != "" || entry.LinkSHA256 != "" || entry.MaterializedLink != "" {
				return errors.New("invalid input manifest directory")
			}
		case "file":
			if entry.Size < 0 || entry.Size > MaxFileBytes || entry.Size > MaxBytes-total || !validDigest(entry.SHA256) || entry.Link != "" || entry.LinkSHA256 != "" || entry.MaterializedLink != "" {
				return errors.New("invalid input manifest file")
			}
			total += entry.Size
		case "symlink":
			if entry.Size != 0 || entry.SHA256 != "" || !validDigest(entry.LinkSHA256) || !validRelativeLink(entry.MaterializedLink) || (entry.Link != "" && !validRelativeLink(entry.Link)) {
				return errors.New("invalid input manifest symlink")
			}
			if entry.Link != "" {
				digest := sha256.Sum256([]byte(entry.Link))
				if hex.EncodeToString(digest[:]) != entry.LinkSHA256 {
					return errors.New("input manifest link identity differs")
				}
			}
		default:
			return errors.New("unsupported input manifest type")
		}
		entries[entry.Path] = entry
	}
	root, ok := entries["."]
	if !ok || root.Kind != "directory" || root.Mode != 0700 || total != manifest.Bytes {
		return errors.New("input manifest root or byte inventory differs")
	}
	for name, entry := range entries {
		if name != "." {
			parent, ok := entries[path.Dir(name)]
			if !ok || parent.Kind != "directory" {
				return errors.New("input manifest omits a real parent directory")
			}
		}
		if entry.Kind == "symlink" {
			target := path.Join(path.Dir(name), entry.MaterializedLink)
			if !validPath(target, true) || target == name {
				return errors.New("input manifest link escapes or loops")
			}
			targetEntry, ok := entries[target]
			if !ok || (targetEntry.Kind == "directory" && (target == "." || nested(target, name))) {
				return errors.New("input manifest link target is absent or recursive")
			}
			visited := map[string]bool{name: true}
			for targetEntry.Kind == "symlink" {
				if visited[target] {
					return errors.New("input manifest link cycle")
				}
				visited[target] = true
				target = path.Join(path.Dir(target), targetEntry.MaterializedLink)
				targetEntry, ok = entries[target]
				if !ok {
					return errors.New("input manifest link target is absent")
				}
			}
		}
	}
	for _, input := range manifest.Inputs {
		if _, ok := entries[path.Join(roots[input.Root].Path, input.Path)]; !ok {
			return errors.New("input manifest omits a declared input")
		}
	}
	identity := manifest.Digest
	manifest.Digest = ""
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != identity {
		return errors.New("input manifest digest differs")
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validRelativeLink(value string) bool {
	return value != "" && len(value) <= 4096 && !strings.HasPrefix(value, "/") && !strings.ContainsAny(value, "\\\x00\r\n:") && path.Clean(value) == value
}

func uniqueKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 16 {
			return errors.New("input manifest nesting limit exceeded")
		}
		token, err := decoder.Token()
		if err != nil {
			return errors.New("invalid input manifest JSON")
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delimiter {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				name, ok := key.(string)
				if err != nil || !ok || keys[name] {
					return errors.New("duplicate or invalid input manifest key")
				}
				keys[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("invalid input manifest object")
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("invalid input manifest array")
			}
		default:
			return errors.New("invalid input manifest delimiter")
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("input manifest contains trailing JSON")
	}
	return nil
}
