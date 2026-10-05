// Package testutil contains independent binary-format fixtures shared by tests.
package testutil

// WASIExit emits a minimal public-format WASI command, requiring no downloaded
// compiler. Tests choose a process outcome independently of Hayaku's planner.
func WASIExit(code byte) []byte {
	if code > 63 {
		panic("fixture exit code must fit signed LEB128")
	}
	name := func(value string) []byte { return append([]byte{byte(len(value))}, []byte(value)...) }
	section := func(id byte, value []byte) []byte { return append([]byte{id, byte(len(value))}, value...) }
	module := []byte{0, 'a', 's', 'm', 1, 0, 0, 0}
	module = append(module, section(1, []byte{2, 0x60, 1, 0x7f, 0, 0x60, 0, 0})...)
	imports := append([]byte{1}, name("wasi_snapshot_preview1")...)
	imports = append(imports, name("proc_exit")...)
	imports = append(imports, 0, 0)
	module = append(module, section(2, imports)...)
	module = append(module, section(3, []byte{1, 1})...)
	module = append(module, section(5, []byte{1, 0, 1})...)
	exports := append([]byte{2}, name("memory")...)
	exports = append(exports, 2, 0)
	exports = append(exports, name("_start")...)
	exports = append(exports, 0, 1)
	module = append(module, section(7, exports)...)
	module = append(module, section(10, []byte{1, 6, 0, 0x41, code, 0x10, 0, 0x0b})...)
	return module
}
