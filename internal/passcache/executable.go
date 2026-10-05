package passcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func ExecutableDigest(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("backend executable is unavailable")
	}
	name, err = filepath.EvalSymlinks(name)
	if err != nil {
		return "", fmt.Errorf("backend executable is unavailable")
	}
	file, err := os.Open(name)
	if err != nil {
		return "", fmt.Errorf("backend executable is unavailable")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > 256<<20 {
		return "", fmt.Errorf("backend executable is not bounded")
	}
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(file, (256<<20)+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || count > 256<<20 || count != after.Size() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", fmt.Errorf("backend executable changed")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
