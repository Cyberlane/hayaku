package startup

import "testing"

func TestInitialized(t *testing.T) {
	if !Enabled() {
		t.Fatal("initialization regression")
	}
}
