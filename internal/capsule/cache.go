package capsule

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/Cyberlane/hayaku/internal/passcache"
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
}
type passCache struct{ cache *passcache.Cache }

func openPassCache(directory string) (*passCache, error) {
	c, err := passcache.Open(directory, "hayaku-wasi-capsule-pass-v1")
	if err != nil || c == nil {
		return nil, err
	}
	return &passCache{cache: c}, nil
}
func (c *passCache) load(result Result, limit int64) (passReceipt, string) {
	if c == nil {
		return passReceipt{}, "disabled"
	}
	data, status := c.cache.Load(result.Key)
	if status != "hit" {
		return passReceipt{}, status
	}
	var r passReceipt
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if len(data) > maxReceiptBytes || d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF {
		return passReceipt{}, "invalid"
	}
	canonical, _ := json.Marshal(r)
	if !bytes.Equal(data, canonical) || r.Schema != 1 || r.Key != result.Key || r.ModuleDigest != result.ModuleDigest || r.InputDigest != result.InputDigest || r.BackendDigest != result.BackendDigest || !validDigest(r.StdoutDigest) || !validDigest(r.StderrDigest) || r.StdoutBytes < 0 || r.StderrBytes < 0 || r.StdoutBytes > limit || r.StderrBytes > limit-r.StdoutBytes {
		return passReceipt{}, "invalid"
	}
	return r, "hit"
}
func (c *passCache) invalidate(key string) error {
	if c == nil {
		return nil
	}
	return c.cache.Invalidate(key)
}
func receiptFromResult(r Result) passReceipt {
	return passReceipt{Schema: 1, Key: r.Key, ModuleDigest: r.ModuleDigest, InputDigest: r.InputDigest, BackendDigest: r.BackendDigest, StdoutDigest: r.StdoutDigest, StderrDigest: r.StderrDigest, StdoutBytes: r.StdoutBytes, StderrBytes: r.StderrBytes}
}
func (c *passCache) store(ctx context.Context, r Result) error {
	if c == nil || !r.Complete || !r.Qualified || !r.Passed {
		return nil
	}
	return c.cache.Store(ctx, r.Key, receiptFromResult(r))
}

func currentExecutableDigest(ctx context.Context) (string, error) {
	return passcache.ExecutableDigest(ctx)
}
