package calc

import "testing"

func TestValue(t *testing.T) {
	if Value() != 42 {
		t.Fatal("literal regression")
	}
}
