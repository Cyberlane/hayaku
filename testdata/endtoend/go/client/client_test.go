package client
import ("os"; "testing")
func TestShared(t *testing.T) { if Shared()!=14 { t.Fatal("shared import") } }
func TestFixture(t *testing.T) { b,e:=os.ReadFile("data/value.txt"); if e!=nil || string(b)!="ready\n" { t.Fatal("fixture") } }
