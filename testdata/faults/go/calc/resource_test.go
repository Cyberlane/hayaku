package calc

import "testing"

func TestResource(t *testing.T) {
	if Resource() != "ready" {
		t.Fatal("embedded resource regression")
	}
}
