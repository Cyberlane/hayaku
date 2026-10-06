package setup
import "testing"
func TestStartup(t *testing.T) { if !ready { t.Fatal("startup") } }
