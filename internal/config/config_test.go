package config

import (
	"strings"
	"testing"
)

const valid = `{"schema":1,"context":{"id":"local","os":"darwin","arch":"arm64"},"workspaces":[{"id":"go","root":".","adapter":"go","command":{"cwd":".","executable":"go","argv":["test","./..."]}}]}`

func TestDecodeStrictPolicy(t *testing.T) {
	if _, err := Decode(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		strings.Replace(valid, `"schema":1`, `"schema":1,"schema":1`, 1),
		strings.Replace(valid, `"schema":1`, `"schema":1,"skip_anyway":true`, 1),
		strings.Replace(valid, `"root":"."`, `"root":"../outside"`, 1),
		valid + `{}`, `null`,
	} {
		if _, err := Decode(strings.NewReader(text)); err == nil {
			t.Fatalf("accepted malformed policy %s", text)
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(valid)
	f.Add(`{"schema":1,"schema":2}`)
	f.Fuzz(func(t *testing.T, s string) {
		c, err := Decode(strings.NewReader(s))
		if err == nil {
			if err := Validate(c); err != nil {
				t.Fatal(err)
			}
			if Digest(c) == "" {
				t.Fatal("emptydigest")
			}
		}
	})
}
