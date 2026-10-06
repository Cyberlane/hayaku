package capsule

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"
)

type memoryFS struct {
	files       map[string][]byte
	directories map[string][]fs.DirEntry
}

func newMemoryFS(files []File) *memoryFS {
	result := &memoryFS{files: make(map[string][]byte, len(files)), directories: map[string][]fs.DirEntry{".": nil}}
	for _, file := range files {
		if file.Directory {
			if _, exists := result.directories[file.Path]; !exists {
				result.directories[file.Path] = nil
			}
		} else {
			result.files[file.Path] = file.Data
			result.directories[path.Dir(file.Path)] = append(result.directories[path.Dir(file.Path)], memoryInfo{name: path.Base(file.Path), size: int64(len(file.Data))})
		}
		for name := path.Dir(file.Path); name != "."; name = path.Dir(name) {
			if _, exists := result.directories[name]; !exists {
				result.directories[name] = nil
			}
		}
	}
	for name := range result.directories {
		if name != "." {
			parent := path.Dir(name)
			result.directories[parent] = append(result.directories[parent], memoryInfo{name: path.Base(name), directory: true})
		}
	}
	for name, entries := range result.directories {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		result.directories[name] = entries
	}
	return result
}

func (m *memoryFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if data, found := m.files[name]; found {
		return &memoryFile{Reader: bytes.NewReader(data), info: memoryInfo{name: path.Base(name), size: int64(len(data))}}, nil
	}
	if entries, found := m.directories[name]; found {
		return &memoryDirectory{info: memoryInfo{name: path.Base(name), directory: true}, entries: entries}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

type memoryInfo struct {
	name      string
	size      int64
	directory bool
}

func (m memoryInfo) Name() string { return m.name }
func (m memoryInfo) Size() int64  { return m.size }
func (m memoryInfo) Mode() fs.FileMode {
	if m.directory {
		return fs.ModeDir | 0555
	}
	return 0444
}
func (m memoryInfo) ModTime() time.Time         { return time.Unix(0, 0).UTC() }
func (m memoryInfo) IsDir() bool                { return m.directory }
func (m memoryInfo) Sys() any                   { return nil }
func (m memoryInfo) Type() fs.FileMode          { return m.Mode().Type() }
func (m memoryInfo) Info() (fs.FileInfo, error) { return m, nil }

type memoryFile struct {
	*bytes.Reader
	info memoryInfo
}

func (m *memoryFile) Close() error               { return nil }
func (m *memoryFile) Stat() (fs.FileInfo, error) { return m.info, nil }

type memoryDirectory struct {
	info    memoryInfo
	entries []fs.DirEntry
	offset  int
}

func (m *memoryDirectory) Close() error               { return nil }
func (m *memoryDirectory) Stat() (fs.FileInfo, error) { return m.info, nil }
func (m *memoryDirectory) Read([]byte) (int, error)   { return 0, fs.ErrInvalid }
func (m *memoryDirectory) ReadDir(count int) ([]fs.DirEntry, error) {
	if m.offset == len(m.entries) && count > 0 {
		return nil, io.EOF
	}
	end := len(m.entries)
	if count > 0 && count < end-m.offset {
		end = m.offset + count
	}
	result := append([]fs.DirEntry(nil), m.entries[m.offset:end]...)
	m.offset = end
	return result, nil
}

// LoadFiles copies explicitly listed regular files beneath root. Symlinks in
// any component and escapes are rejected; os.Root also enforces confinement
// during races. Guest execution subsequently uses memory, never these paths.
func LoadFiles(ctx context.Context, directory string, names []string) ([]File, error) {
	if len(names) > MaxInputFiles {
		return nil, fmt.Errorf("capsule file count exceeds bound")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("capsule input root is not an accessible directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("capsule input root is unavailable")
	}
	defer root.Close()
	files := make([]File, 0, len(names))
	seen := make(map[string]bool, len(names))
	remaining := int64(MaxInputBytes)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !validInputPath(name) || seen[name] {
			return nil, fmt.Errorf("capsule file path is invalid or duplicated")
		}
		seen[name] = true
		if err := regularComponents(root, name); err != nil {
			return nil, err
		}
		before, err := root.Lstat(name)
		if err != nil || !before.Mode().IsRegular() {
			return nil, fmt.Errorf("capsule input is not an accessible regular file")
		}
		data, err := loadRegularFile(ctx, root, name, before, remaining)
		if err != nil {
			return nil, err
		}
		if err := regularComponents(root, name); err != nil {
			return nil, err
		}
		remaining -= int64(len(data))
		files = append(files, File{Path: name, Data: data})
	}
	return files, ctx.Err()
}

// loadRegularFile preserves root confinement and the exact admitted regular
// file identity. Its separated preflight is also exercised against real FIFO
// substitution without giving a test hook authority over production reads.
func loadRegularFile(ctx context.Context, root *os.Root, name string, before fs.FileInfo, remaining int64) ([]byte, error) {
	var captured bytes.Buffer
	if err := copyRegularFile(ctx, root, name, before, remaining, &captured); err != nil {
		return nil, err
	}
	return captured.Bytes(), nil
}
func copyRegularFile(ctx context.Context, root *os.Root, name string, before fs.FileInfo, remaining int64, destination io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if before == nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > remaining {
		return fmt.Errorf("capsule input exceeds byte bound or is nonregular")
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("capsule input cannot be opened")
	}
	opened, statErr := file.Stat()
	if statErr != nil || !sameInputFile(before, opened) {
		file.Close()
		return fmt.Errorf("capsule input changed during capture")
	}
	read, readErr := io.CopyBuffer(destination, inputReader{ctx, io.LimitReader(file, before.Size()+1)}, make([]byte, 32<<10))
	after, statErr := file.Stat()
	closeErr := file.Close()
	current, pathErr := root.Lstat(name)
	if readErr != nil || closeErr != nil || statErr != nil || pathErr != nil || read != before.Size() || !sameInputFile(before, after) || !sameInputFile(before, current) {
		return fmt.Errorf("capsule input capture is incomplete or changed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
func sameInputFile(before, after fs.FileInfo) bool {
	return after != nil && after.Mode().IsRegular() && os.SameFile(before, after) && before.Mode() == after.Mode() && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

type inputReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r inputReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func regularComponents(root *os.Root, name string) error {
	parts := strings.Split(name, "/")
	for i := range parts {
		component := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(component)
		if err != nil || info.Mode()&fs.ModeSymlink != 0 || (i+1 < len(parts) && !info.IsDir()) {
			return fmt.Errorf("capsule input has an inaccessible or linked component")
		}
	}
	return nil
}
