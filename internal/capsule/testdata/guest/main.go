package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

func fail() { os.Exit(1) }

func main() {
	if len(os.Args) < 2 {
		fail()
	}
	switch os.Args[1] {
	case "deterministic":
		data, err := os.ReadFile("inputs/read.txt")
		if err != nil || string(data) != "value" {
			os.Exit(11)
		}
		if _, err := os.Stat("inputs/missing.txt"); !errors.Is(err, fs.ErrNotExist) {
			os.Exit(12)
		}
		entries, err := os.ReadDir("inputs")
		if err != nil {
			os.Exit(13)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if strings.Join(names, ",") != "a.txt,read.txt,z.txt" {
			os.Exit(14)
		}
		info, err := os.Stat("inputs/read.txt")
		// WASI preview1 exposes type/timestamps, not POSIX permissions. Go
		// synthesizes 0600 for regular files; writes are separately denied.
		if err != nil || info.Mode().Perm() != 0600 || info.ModTime().UnixNano() != 0 {
			os.Exit(15)
		}
		entropy := make([]byte, 24)
		if _, err := rand.Read(entropy); err != nil {
			os.Exit(16)
		}
		now := time.Now().UnixNano()
		time.Sleep(10 * time.Millisecond)
		fmt.Printf("%s|%s|%x|%d|%d\n", data, os.Getenv("VALUE"), entropy, now, time.Now().UnixNano())
	case "escape":
		if len(os.Args) != 3 {
			fail()
		}
		if _, err := os.ReadFile(os.Args[2]); !errors.Is(err, fs.ErrNotExist) || os.Getenv("HAYAKU_HOST_ONLY_SECRET") != "" {
			fail()
		}
		fmt.Println("host input unavailable")
	case "caught-write":
		if os.WriteFile("inputs/read.txt", []byte("changed"), 0600) == nil {
			fail()
		}
		fmt.Println("guest caught errno")
	case "bundle":
		if len(os.Args) != 3 {
			fail()
		}
		generated, err := os.ReadFile("generated/value.txt")
		if err != nil || string(generated) != "generated" {
			fail()
		}
		linked, err := os.ReadFile("node_modules/pkg/src/value.txt")
		if err != nil || string(linked) != os.Args[2] {
			fail()
		}
		alias, err := os.ReadFile("generated/alias.txt")
		if err != nil || string(alias) != os.Args[2] {
			fail()
		}
		empty, err := os.Stat("node_modules/pkg/src/empty")
		if err != nil || !empty.IsDir() {
			fail()
		}
		fmt.Printf("%s|%s|%s\n", generated, linked, alias)
	case "fail":
		fail()
	default:
		fail()
	}
}
