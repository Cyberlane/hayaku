package capsule

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

const maxReceiptBytes = 8192

type passReceipt struct {
	Schema        int    `json:"schema"`
	Key           string `json:"key"`
	ModuleDigest  string `json:"module_digest"`
	InputDigest   string `json:"input_digest"`
	BackendDigest string `json:"backend_digest"`
	StdoutDigest  string `json:"stdout_digest"`
	StderrDigest  string `json:"stderr_digest"`
	StdoutBytes   int64  `json:"stdout_bytes"`
	StderrBytes   int64  `json:"stderr_bytes"`
	MAC           string `json:"mac"`
}

type passCache struct {
	root *os.Root
	key  []byte
}

// Cache authentication assumes the invoking user and the Hayaku executable
// are trusted. It rejects accidental/guest/foreign JSON tampering; it cannot
// protect a user from someone who can replace their executable or private key.
func openPassCache(directory string) (*passCache, error) {
	if directory == "" {
		return nil, nil
	}
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("capsule cache requires an independently qualified Windows ACL backend")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("capsule cache directory is unavailable")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("capsule cache directory is not private")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("capsule cache root is unavailable")
	}
	cache := &passCache{root: root}
	key, err := readPrivate(root, "authority.key", 32)
	if errors.Is(err, fs.ErrNotExist) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err == nil {
			file, createErr := root.OpenFile("authority.key", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if createErr == nil {
				_, writeErr := file.Write(key)
				syncErr := file.Sync()
				closeErr := file.Close()
				if writeErr != nil || syncErr != nil || closeErr != nil {
					err = fmt.Errorf("capsule authority key could not be persisted")
				}
			} else if errors.Is(createErr, fs.ErrExist) {
				key, err = readPrivate(root, "authority.key", 32)
			} else {
				err = createErr
			}
		}
	}
	if err != nil || len(key) != 32 {
		root.Close()
		return nil, fmt.Errorf("capsule cache authority is unavailable")
	}
	cache.key = key
	return cache, nil
}

func readPrivate(root *os.Root, name string, limit int64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, fmt.Errorf("capsule cache object is not private and bounded")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("capsule cache object changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("capsule cache object exceeds size bound")
	}
	current, err := root.Lstat(name)
	if err != nil || !current.Mode().IsRegular() || current.Mode().Perm()&0077 != 0 || !os.SameFile(opened, current) || int64(len(data)) != current.Size() {
		return nil, fmt.Errorf("capsule cache object changed")
	}
	return data, nil
}

func (c *passCache) load(result Result, limit int64) (passReceipt, string) {
	if c == nil {
		return passReceipt{}, "disabled"
	}
	data, err := readPrivate(c.root, result.Key+".json", maxReceiptBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return passReceipt{}, "miss"
	}
	if err != nil {
		return passReceipt{}, "invalid"
	}
	var receipt passReceipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil || decoder.Decode(new(any)) != io.EOF {
		return passReceipt{}, "invalid"
	}
	canonical, _ := json.Marshal(receipt)
	if !bytes.Equal(data, canonical) || receipt.Schema != 1 || receipt.Key != result.Key || receipt.ModuleDigest != result.ModuleDigest || receipt.InputDigest != result.InputDigest || receipt.BackendDigest != result.BackendDigest || !validDigest(receipt.StdoutDigest) || !validDigest(receipt.StderrDigest) || receipt.StdoutBytes < 0 || receipt.StderrBytes < 0 || receipt.StdoutBytes > limit || receipt.StderrBytes > limit-receipt.StdoutBytes || !validDigest(receipt.MAC) {
		return passReceipt{}, "invalid"
	}
	provided, _ := hex.DecodeString(receipt.MAC)
	if !hmac.Equal(provided, c.authenticate(receipt)) {
		return passReceipt{}, "invalid"
	}
	return receipt, "hit"
}

func (c *passCache) authenticate(receipt passReceipt) []byte {
	receipt.MAC = ""
	data, _ := json.Marshal(receipt)
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte("hayaku-capsule-local-pass-v1\x00"))
	mac.Write(data)
	return mac.Sum(nil)
}

func (c *passCache) invalidate(key string) error {
	if c == nil {
		return nil
	}
	err := c.root.Remove(key + ".json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func receiptFromResult(result Result) passReceipt {
	return passReceipt{Schema: 1, Key: result.Key, ModuleDigest: result.ModuleDigest, InputDigest: result.InputDigest, BackendDigest: result.BackendDigest, StdoutDigest: result.StdoutDigest, StderrDigest: result.StderrDigest, StdoutBytes: result.StdoutBytes, StderrBytes: result.StderrBytes}
}

func (c *passCache) store(ctx context.Context, result Result) error {
	if c == nil || !result.Complete || !result.Qualified || !result.Passed {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	receipt := receiptFromResult(result)
	receipt.MAC = hex.EncodeToString(c.authenticate(receipt))
	data, _ := json.Marshal(receipt)
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return fmt.Errorf("capsule cache temporary identity failed")
	}
	name := ".pass-" + hex.EncodeToString(random)
	file, err := c.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("capsule pass cannot be persisted")
	}
	defer c.root.Remove(name)
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return fmt.Errorf("capsule pass cannot be persisted")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.root.Rename(name, result.Key+".json"); err != nil {
		return fmt.Errorf("capsule pass cannot be published")
	}
	if err := ctx.Err(); err != nil {
		_ = c.invalidate(result.Key)
		return err
	}
	return nil
}

func currentExecutableDigest(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("capsule backend executable is unavailable")
	}
	name, err = filepath.EvalSymlinks(name)
	if err != nil {
		return "", fmt.Errorf("capsule backend executable is unavailable")
	}
	file, err := os.Open(name)
	if err != nil {
		return "", fmt.Errorf("capsule backend executable is unavailable")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > 256<<20 {
		return "", fmt.Errorf("capsule backend executable is not bounded")
	}
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(file, (256<<20)+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || count > 256<<20 || count != after.Size() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", fmt.Errorf("capsule backend executable changed")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
