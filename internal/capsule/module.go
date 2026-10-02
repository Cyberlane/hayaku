package capsule

import (
	"bytes"
	"fmt"
)

// The interpreter memory-page limit does not constrain reference tables. The
// WASI contract also fixes one private table and at most 262144 references.
// Existing smaller declared maxima are preserved. Adding/clamping a maximum
// is an explicit resource semantic of this backend, never native equivalence.
const maxTableElements = 262144

func boundModuleTables(module []byte) ([]byte, error) {
	if len(module) < 8 || !bytes.Equal(module[:8], []byte{0, 'a', 's', 'm', 1, 0, 0, 0}) {
		return nil, fmt.Errorf("capsule requires a WebAssembly version1 module")
	}
	output := append([]byte(nil), module[:8]...)
	reader := moduleReader{data: module[8:]}
	tableSection := false
	for len(reader.data) > 0 {
		id, err := reader.byte()
		if err != nil {
			return nil, err
		}
		size, err := reader.uint32()
		if err != nil || uint64(size) > uint64(len(reader.data)) {
			return nil, fmt.Errorf("capsule module has an invalid section bound")
		}
		payload := reader.data[:int(size)]
		reader.data = reader.data[int(size):]
		switch id {
		case 2:
			if err := privateImports(payload); err != nil {
				return nil, err
			}
		case 4:
			if tableSection {
				return nil, fmt.Errorf("capsule module repeats its table section")
			}
			tableSection = true
			payload, err = boundedTable(payload)
			if err != nil {
				return nil, err
			}
		}
		output = append(output, id)
		output = append(output, encodeUint32(uint32(len(payload)))...)
		output = append(output, payload...)
	}
	return output, nil
}

func privateImports(payload []byte) error {
	reader := moduleReader{data: payload}
	count, err := reader.uint32()
	if err != nil || count > 256 {
		return fmt.Errorf("capsule import count exceeds bound")
	}
	for i := uint32(0); i < count; i++ {
		if err := reader.skipName(); err != nil {
			return err
		}
		if err := reader.skipName(); err != nil {
			return err
		}
		kind, err := reader.byte()
		if err != nil || kind != 0 {
			return fmt.Errorf("capsule cannot import external memory, tables, globals or tags")
		}
		if _, err := reader.uint32(); err != nil {
			return err
		}
	}
	if len(reader.data) != 0 {
		return fmt.Errorf("capsule import section contains trailing data")
	}
	return nil
}

func boundedTable(payload []byte) ([]byte, error) {
	reader := moduleReader{data: payload}
	count, err := reader.uint32()
	if err != nil || count > 1 {
		return nil, fmt.Errorf("capsule requires at most one private reference table")
	}
	output := encodeUint32(count)
	if count == 1 {
		reference, err := reader.byte()
		if err != nil || (reference != 0x70 && reference != 0x6f) {
			return nil, fmt.Errorf("capsule table has an unsupported reference type")
		}
		flags, err := reader.byte()
		if err != nil || flags > 1 {
			return nil, fmt.Errorf("capsule table has unsupported limits")
		}
		minimum, err := reader.uint32()
		if err != nil || minimum > maxTableElements {
			return nil, fmt.Errorf("capsule table minimum exceeds bound")
		}
		maximum := uint32(maxTableElements)
		if flags == 1 {
			declared, err := reader.uint32()
			if err != nil || declared < minimum {
				return nil, fmt.Errorf("capsule table limits are invalid")
			}
			if declared < maximum {
				maximum = declared
			}
		}
		output = append(output, reference, 1)
		output = append(output, encodeUint32(minimum)...)
		output = append(output, encodeUint32(maximum)...)
	}
	if len(reader.data) != 0 {
		return nil, fmt.Errorf("capsule table section contains trailing data")
	}
	return output, nil
}

type moduleReader struct{ data []byte }

func (r *moduleReader) byte() (byte, error) {
	if len(r.data) == 0 {
		return 0, fmt.Errorf("capsule module is truncated")
	}
	value := r.data[0]
	r.data = r.data[1:]
	return value, nil
}

func (r *moduleReader) uint32() (uint32, error) {
	var value uint32
	for i := 0; i < 5; i++ {
		part, err := r.byte()
		if err != nil || (i == 4 && part > 15) {
			return 0, fmt.Errorf("capsule module has an invalid integer bound")
		}
		value |= uint32(part&127) << (7 * i)
		if part&128 == 0 {
			return value, nil
		}
	}
	return 0, fmt.Errorf("capsule module integer exceeds bound")
}

func (r *moduleReader) skipName() error {
	size, err := r.uint32()
	if err != nil || size > 4096 || uint64(size) > uint64(len(r.data)) {
		return fmt.Errorf("capsule import name exceeds bound")
	}
	r.data = r.data[int(size):]
	return nil
}

func encodeUint32(value uint32) []byte {
	var result []byte
	for {
		part := byte(value & 127)
		value >>= 7
		if value != 0 {
			part |= 128
		}
		result = append(result, part)
		if value == 0 {
			return result
		}
	}
}
