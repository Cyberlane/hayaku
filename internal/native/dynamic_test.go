package native

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A small independently encoded ELF layout exercises parser admission only.
// It is not an executable runtime fixture or evidence of loader equivalence.
func dynamicFixtureBytes(interpreter, soname string, needed []string) []byte {
	data := make([]byte, 8192)
	copy(data, "\x7fELF")
	data[4] = 2
	data[5] = 1
	data[6] = 1
	put16 := func(at int, value uint16) { binary.LittleEndian.PutUint16(data[at:at+2], value) }
	put32 := func(at int, value uint32) { binary.LittleEndian.PutUint32(data[at:at+4], value) }
	put64 := func(at int, value uint64) { binary.LittleEndian.PutUint64(data[at:at+8], value) }
	put16(16, uint16(elf.ET_DYN))
	put16(18, uint16(elf.EM_AARCH64))
	put32(20, 1)
	put64(24, 0x400080)
	put64(32, 64)
	put16(52, 64)
	put16(54, 56)
	count := 3
	if interpreter != "" {
		count++
	}
	put16(56, uint16(count))
	program := func(index int, kind elf.ProgType, flags uint32, offset, size uint64) {
		at := 64 + 56*index
		put32(at, uint32(kind))
		put32(at+4, flags)
		put64(at+8, offset)
		put64(at+16, 0x400000+offset)
		put64(at+32, size)
		put64(at+40, size)
		put64(at+48, 1)
	}
	program(0, elf.PT_LOAD, 5, 0, uint64(len(data)))
	program(2, elf.PT_GNU_STACK, 6, 0, 0)
	if interpreter != "" {
		program(3, elf.PT_INTERP, 4, 512, uint64(len(interpreter)+1))
		copy(data[512:], interpreter)
	}
	table := []byte{0}
	appendName := func(name string) uint64 {
		offset := uint64(len(table))
		table = append(table, []byte(name)...)
		table = append(table, 0)
		return offset
	}
	tags := [][2]uint64{{uint64(elf.DT_STRTAB), 0x401000}}
	if soname != "" {
		tags = append(tags, [2]uint64{uint64(elf.DT_SONAME), appendName(soname)})
	}
	for _, name := range needed {
		tags = append(tags, [2]uint64{uint64(elf.DT_NEEDED), appendName(name)})
	}
	tags = append(tags, [2]uint64{uint64(elf.DT_STRSZ), uint64(len(table))}, [2]uint64{})
	for index, pair := range tags {
		put64(1024+index*16, pair[0])
		put64(1032+index*16, pair[1])
	}
	program(1, elf.PT_DYNAMIC, 4, 1024, uint64(len(tags)*16))
	copy(data[4096:], table)
	return data
}
func dynamicFixture(t *testing.T) (*Runtime, *DynamicProgram, string) {
	t.Helper()
	dir := t.TempDir()
	for name, data := range map[string][]byte{
		"loader":  dynamicFixtureBytes("", "ldfixture.so", nil),
		"library": dynamicFixtureBytes("", "libfixture.so", []string{"ldfixture.so"}),
		"program": dynamicFixtureBytes("/lib/ldfixture.so", "", []string{"libfixture.so"}),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0555); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := CaptureRuntime(context.Background(), RuntimeRequest{Loader: filepath.Join(dir, "loader"), Libraries: []RuntimeLibrary{{Name: "libfixture.so", Path: filepath.Join(dir, "library")}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	program, err := CaptureDynamicProgram(context.Background(), DynamicProgramRequest{Program: filepath.Join(dir, "program"), Args: []string{"first"}, Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := program.Close(); err != nil {
			t.Error(err)
		}
	})
	return runtime, program, dir
}
func TestDynamicCaptureBindsClosedRuntimeAndInitialArguments(t *testing.T) {
	runtime, program, _ := dynamicFixture(t)
	if err := runtime.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := program.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtime.loaderName() != "ldfixture.so" || program.primary.image.interpreter != "/lib/ldfixture.so" || runtime.bytes != 16384 {
		t.Fatal("independent loader and byte identities lost")
	}
	args := []string{"unchanged"}
	p, err := CaptureDynamicProgram(context.Background(), DynamicProgramRequest{Program: program.primary.source, Args: args, Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	args[0] = "caller mutation"
	if err := p.Verify(context.Background()); err != nil || p.args[0] != "unchanged" {
		t.Fatal("initial argv was not privately copied", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Verify(context.Background()); err == nil {
		t.Fatal("closed borrowed runtime remained usable")
	}
}
func TestDynamicCaptureRejectsSourceCopyAndPrivateDrift(t *testing.T) {
	for _, fault := range []string{"source-bytes", "source-replacement", "copy-bytes", "copy-mode", "copy-membership", "source-alias", "private-args", "private-elf"} {
		t.Run(fault, func(t *testing.T) {
			runtime, program, dir := dynamicFixture(t)
			switch fault {
			case "source-bytes", "copy-bytes":
				name := program.primary.source
				if fault == "copy-bytes" {
					name = program.primary.copy
				}
				info, err := os.Lstat(name)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.Chmod(name, 0600); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				data[len(data)-1] ^= 1
				if err = os.WriteFile(name, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Chmod(name, info.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
				if err = os.Chtimes(name, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "source-replacement":
				original := program.primary.source
				info, err := os.Lstat(original)
				if err != nil {
					t.Fatal(err)
				}
				replacement := filepath.Join(dir, "replacement")
				data, err := os.ReadFile(original)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(replacement, data, info.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
				if err = os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(replacement, original); err != nil {
					t.Fatal(err)
				}
			case "copy-mode":
				if err := os.Chmod(program.primary.copy, 0500); err != nil {
					t.Fatal(err)
				}
			case "copy-membership":
				if err := os.WriteFile(filepath.Join(runtime.dir, "unexpected"), []byte("outside manifest"), 0400); err != nil {
					t.Fatal(err)
				}
			case "source-alias":
				name := program.primary.source
				target := filepath.Join(dir, "alias-target")
				if err := os.Rename(name, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, name); err != nil {
					t.Fatal(err)
				}
			case "private-args":
				program.args[0] = "changed"
			case "private-elf":
				program.primary.image.needed = nil
			}
			if err := program.Verify(context.Background()); err == nil {
				t.Fatal("changed admitted runtime remained usable")
			}
		})
	}
}
func TestDynamicRuntimeRejectsUnclosedRolesAndRequestBounds(t *testing.T) {
	dir := t.TempDir()
	loader := filepath.Join(dir, "loader")
	library := filepath.Join(dir, "library")
	program := filepath.Join(dir, "program")
	write := func(name string, data []byte) {
		t.Helper()
		if _, err := os.Lstat(name); err == nil {
			if err := os.Chmod(name, 0755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(name, data, 0555); err != nil {
			t.Fatal(err)
		}
	}
	write(loader, dynamicFixtureBytes("", "ldfixture.so", nil))
	write(library, dynamicFixtureBytes("", "libfixture.so", []string{"missing.so"}))
	write(program, dynamicFixtureBytes("/lib/other.so", "", nil))
	if r, err := CaptureRuntime(context.Background(), RuntimeRequest{Loader: loader, Libraries: []RuntimeLibrary{{"libfixture.so", library}}}); err == nil {
		r.Close()
		t.Fatal("unclosed DSO dependency admitted")
	}
	if _, err := CaptureRuntime(context.Background(), RuntimeRequest{Loader: loader, Libraries: []RuntimeLibrary{{"../bad", library}}}); err == nil {
		t.Fatal("library path name admitted")
	}
	if _, err := CaptureRuntime(context.Background(), RuntimeRequest{Loader: loader, Libraries: make([]RuntimeLibrary, dynamicLibraryLimit+1)}); err == nil {
		t.Fatal("unbounded declaration count admitted")
	}
	if _, err := CaptureRuntime(context.Background(), RuntimeRequest{Loader: strings.Repeat("x", 4097)}); err == nil {
		t.Fatal("unbounded path admitted")
	}
	write(library, dynamicFixtureBytes("", "libfixture.so", nil))
	r, err := CaptureRuntime(context.Background(), RuntimeRequest{Loader: loader, Libraries: []RuntimeLibrary{{"libfixture.so", library}}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if p, err := CaptureDynamicProgram(context.Background(), DynamicProgramRequest{Program: program, Runtime: r}); err == nil {
		p.Close()
		t.Fatal("wrong original interpreter basename admitted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CaptureRuntime(cancelled, RuntimeRequest{Loader: loader}); err == nil {
		t.Fatal("cancelled capture admitted")
	}
}
func TestDynamicELFParserRejectsUnsafeOrAmbiguousTables(t *testing.T) {
	healthy := dynamicFixtureBytes("/lib/ldfixture.so", "", []string{"libfixture.so"})
	if image, err := readDynamicELF(context.Background(), bytes.NewReader(healthy), int64(len(healthy))); err != nil || image.architecture != "arm64" || len(image.needed) != 1 {
		t.Fatal("healthy independently encoded ELF", image, err)
	}
	for _, fault := range []string{"program-count", "program-offset-overflow", "wx", "stack", "memory-size", "dynamic-address", "string-size", "unterminated", "slash-needed", "origin", "rpath", "duplicate-singleton", "foreign"} {
		t.Run(fault, func(t *testing.T) {
			data := append([]byte(nil), healthy...)
			put64 := func(at int, value uint64) { binary.LittleEndian.PutUint64(data[at:at+8], value) }
			switch fault {
			case "program-count":
				binary.LittleEndian.PutUint16(data[56:58], 65535)
			case "program-offset-overflow":
				put64(32, ^uint64(0)-4)
			case "wx":
				binary.LittleEndian.PutUint32(data[68:72], 7)
			case "stack":
				binary.LittleEndian.PutUint32(data[64+112+4:], 7)
			case "memory-size":
				put64(64+40, 1)
			case "dynamic-address":
				put64(64+56+16, 0x500000)
			case "string-size":
				put64(1032+32, ^uint64(0))
			case "unterminated":
				for i := 4096; i < len(data); i++ {
					data[i] = 'a'
				}
			case "slash-needed":
				copy(data[4097:], "../fixture.so")
			case "origin":
				put64(1024+16, uint64(elf.DT_FLAGS))
				put64(1032+16, uint64(elf.DF_ORIGIN))
			case "rpath":
				put64(1024+16, uint64(elf.DT_RPATH))
			case "duplicate-singleton":
				put64(1024+16, uint64(elf.DT_STRTAB))
			case "foreign":
				binary.LittleEndian.PutUint16(data[18:20], uint16(elf.EM_ARM))
			}
			if _, err := readDynamicELF(context.Background(), bytes.NewReader(data), int64(len(data))); err == nil {
				t.Fatal("unsafe ELF admitted")
			}
		})
	}
	// The parser must notice cancellation without visiting any advertised table.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := readDynamicELF(ctx, bytes.NewReader(healthy), int64(len(healthy))); err == nil {
		t.Fatal("cancelled parser admitted")
	}
}

func TestDynamicFinalIdentitySweepRejectsEarlierLastingDrift(t *testing.T) {
	for _, view := range []string{"source", "copy"} {
		for _, fault := range []string{"mode", "mtime", "replacement"} {
			t.Run(view+"-"+fault, func(t *testing.T) {
				r, _, _ := dynamicFixture(t)
				files := r.files()
				if err := files[0].verify(context.Background()); err != nil {
					t.Fatal(err)
				}
				if err := files[1].verify(context.Background()); err != nil {
					t.Fatal(err)
				}
				if err := verifyDynamicIdentities(context.Background(), files); err != nil {
					t.Fatal(err)
				}
				name := files[0].source
				if view == "copy" {
					name = files[0].copy
				}
				before, err := os.Lstat(name)
				if err != nil {
					t.Fatal(err)
				}
				parent := filepath.Dir(name)
				parentBefore, err := os.Lstat(parent)
				if err != nil {
					t.Fatal(err)
				}
				switch fault {
				case "mode":
					err = os.Chmod(name, 0600)
				case "mtime":
					err = os.Chtimes(name, before.ModTime(), before.ModTime().Add(time.Second))
				case "replacement":
					data, readErr := os.ReadFile(name)
					if readErr != nil {
						t.Fatal(readErr)
					}
					replacement := filepath.Join(t.TempDir(), "replacement")
					if err = os.WriteFile(replacement, data, before.Mode().Perm()); err != nil {
						t.Fatal(err)
					}
					if err = os.Chtimes(replacement, before.ModTime(), before.ModTime()); err != nil {
						t.Fatal(err)
					}
					if err = os.Rename(replacement, name); err != nil {
						t.Fatal(err)
					}
					err = os.Chtimes(parent, parentBefore.ModTime(), parentBefore.ModTime())
				}
				if err != nil {
					t.Fatal(err)
				}
				parentAfter, err := os.Lstat(parent)
				if err != nil || !sameProgramInfo(parentBefore, parentAfter) {
					t.Fatal("fixture changed parent metadata", err)
				}
				if err := verifyDynamicIdentities(context.Background(), files); err == nil {
					t.Fatal("earlier lasting file drift passed final aggregate gate")
				}
			})
		}
	}
}

func TestDynamicLibraryInterpreterIsBoundToCapturedLoader(t *testing.T) {
	for _, interpreter := range []string{"/lib/ldfixture.so", "/lib/foreign.so"} {
		t.Run(filepath.Base(interpreter), func(t *testing.T) {
			dir := t.TempDir()
			loader := filepath.Join(dir, "loader")
			library := filepath.Join(dir, "library")
			if err := os.WriteFile(loader, dynamicFixtureBytes("", "ldfixture.so", nil), 0555); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(library, dynamicFixtureBytes(interpreter, "libfixture.so", nil), 0555); err != nil {
				t.Fatal(err)
			}
			r, err := CaptureRuntime(context.Background(), RuntimeRequest{Loader: loader, Libraries: []RuntimeLibrary{{"libfixture.so", library}}})
			if interpreter == "/lib/foreign.so" {
				if err == nil {
					r.Close()
					t.Fatal("foreign DSO interpreter admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if err := r.Verify(context.Background()); err != nil {
				t.Fatal(err)
			}
			if r.libraries["libfixture.so"].image.interpreter != interpreter {
				t.Fatal("full DSO interpreter identity lost")
			}
		})
	}
}
func TestDynamicTransportRejectsCopiedBytesModeAndMembership(t *testing.T) {
	for _, fault := range []string{"bytes", "mode", "membership"} {
		t.Run(fault, func(t *testing.T) {
			_, p, _ := dynamicFixture(t)
			directory := filepath.Join(t.TempDir(), "runtime")
			files, err := p.materialize(context.Background(), directory, "arm64")
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyDynamicTransport(context.Background(), files); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "bytes":
				info, err := os.Lstat(files[0].path)
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(files[0].path)
				if err != nil {
					t.Fatal(err)
				}
				data[len(data)-1] ^= 1
				if err := os.Chmod(files[0].path, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(files[0].path, data, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(files[0].path, info.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(files[0].path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(files[0].path, 0755); err != nil {
					t.Fatal(err)
				}
			case "membership":
				if err := os.WriteFile(filepath.Join(directory, "unbound.so"), []byte("extra"), 0444); err != nil {
					t.Fatal(err)
				}
			}
			if err := verifyDynamicTransport(context.Background(), files); err == nil {
				t.Fatal("transport drift accepted")
			}
		})
	}
}

const dynamicCSource = `
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/mman.h>
#include <sys/ioctl.h>
#include <sys/syscall.h>
#include <sys/socket.h>
#include <sys/stat.h>
static void check(const char *name,int ok){if(!ok){fprintf(stderr,"dynamic-fault:%s errno=%d\n",name,errno);exit(71);}}
static void nonblocking_control(int fd){
 int before=fcntl(fd,F_GETFL);check("fionbio-original-flags",before>=0);
 int mode=1;check("fionbio-set",ioctl(fd,FIONBIO,&mode)==0);check("fionbio-set-flags",fcntl(fd,F_GETFL)&O_NONBLOCK);
 mode=0;check("fionbio-clear",ioctl(fd,FIONBIO,&mode)==0);check("fionbio-clear-flags",!(fcntl(fd,F_GETFL)&O_NONBLOCK));
 mode=(before&O_NONBLOCK)!=0;check("fionbio-restore",ioctl(fd,FIONBIO,&mode)==0);
}
static void *original_thread(void *unused){puts("original-thread");return NULL;}
static void *thread(void *unused){
 char value[8];int fd=open("data/value",O_RDONLY);check("thread-resource",fd>=0&&read(fd,value,7)==7&&!memcmp(value,"fixture",7));close(fd);
 fd=open("/etc/os-release",O_RDONLY);check("thread-outside-read-denied",fd<0&&(errno==EACCES||errno==EPERM));
 fd=socket(AF_INET,SOCK_STREAM,0);check("thread-network-denied",fd<0&&errno==EPERM);
 return NULL;
}
int main(int argc,char **argv){
 if(argc==2&&!strcmp(argv[1],"original")){for(int fd=0;fd<3;fd++)nonblocking_control(fd);pthread_t t;check("original-thread",pthread_create(&t,NULL,original_thread,NULL) == 0);check("original-join",pthread_join(t,NULL)==0);puts("original-dynamic-positive");return 0;}
 check("argv",argc==2&&!strcmp(argv[0],"/runtime/#program")&&!strcmp(argv[1],"inside"));
 check("env",getenv("DECLARED")&&!strcmp(getenv("DECLARED"),"fixture")&&!getenv("PATH")&&!getenv("LD_LIBRARY_PATH"));
 char cwd[128];check("cwd",getcwd(cwd,sizeof(cwd))&&!strcmp(cwd,"/resources"));
 char value;check("empty-stdin",read(0,&value,1)==0);
 for(int fd=3;fd<32;fd++){errno=0;check("closed-fd",fcntl(fd,F_GETFD)==-1&&errno==EBADF);}
 for(int fd=0;fd<3;fd++)nonblocking_control(fd);
 errno=0;check("fionbio-invalid-pointee",ioctl(1,FIONBIO,NULL)<0&&errno==EFAULT);
 int mode=1;check("ioctl-other-request",ioctl(1,FIONREAD,&mode)<0&&errno==EPERM);
 check("ioctl-async-denied",ioctl(1,FIOASYNC,&mode)<0&&errno==EPERM);
 check("ioctl-high-fd",syscall(SYS_ioctl,(1ULL<<32)|1,FIONBIO,&mode)<0&&errno==EPERM);
 check("ioctl-high-request",syscall(SYS_ioctl,1,(1ULL<<32)|FIONBIO,&mode)<0&&errno==EPERM);
 puts("dynamic-positive-before-denials");fflush(stdout);
 pthread_t t;check("pthread-create",pthread_create(&t,NULL,thread,NULL)==0);check("pthread-join",pthread_join(t,NULL)==0);
 int fd=open("code",O_RDONLY);check("resource-elf-open",fd>=0);
 check("ioctl-other-fd",ioctl(fd,FIONBIO,&mode)<0&&errno==EPERM);
 struct stat before,after;check("resource-stat",fstat(fd,&before)==0);int saved=dup(0);check("stdin-save",saved>=3);check("resource-remap",dup2(fd,0)==0);
 nonblocking_control(0);check("resource-stat-after",fstat(0,&after)==0);
 check("resource-metadata-unchanged",before.st_dev==after.st_dev&&before.st_ino==after.st_ino&&before.st_mode==after.st_mode&&before.st_size==after.st_size&&before.st_mtim.tv_sec==after.st_mtim.tv_sec&&before.st_mtim.tv_nsec==after.st_mtim.tv_nsec);
 char magic[4];check("resource-bytes-unchanged",read(0,magic,4)==4&&!memcmp(magic,"\177ELF",4));
 void *rx=mmap(NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE,0,0);check("resource-noexec",rx==MAP_FAILED&&(errno==EPERM||errno==EACCES));
 check("stdin-restore",dup2(saved,0)==0);close(saved);close(fd);
 fd=open("/scratch/code",O_CREAT|O_WRONLY,0555);check("scratch-open",fd>=0);char block[4096]={0};check("scratch-write",write(fd,block,sizeof(block))==sizeof(block));close(fd);
 fd=open("/scratch/code",O_RDONLY);check("scratch-read",fd>=0);rx=mmap(NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE,fd,0);check("scratch-noexec",rx==MAP_FAILED&&(errno==EPERM||errno==EACCES));close(fd);
 rx=mmap(NULL,4096,PROT_READ|PROT_EXEC,MAP_PRIVATE|MAP_ANONYMOUS,-1,0);check("anonymous-noexec",rx==MAP_FAILED&&errno==EPERM);
 void *rw=mmap(NULL,4096,PROT_READ|PROT_WRITE,MAP_PRIVATE|MAP_ANONYMOUS,-1,0);check("rw-positive",rw!=MAP_FAILED);check("mprotect-noexec",mprotect(rw,4096,PROT_READ|PROT_EXEC)<0&&errno==EPERM);munmap(rw,4096);
 check("chmod-denied",chmod("data/value",0444)<0&&errno==EPERM);
 check("fork-denied",fork()<0&&errno==EPERM);
 puts("dynamic-thread-and-noexec-positive");return 0;
}
`

func TestInstalledLinuxDynamicProgramAndPthread(t *testing.T) {
	image := os.Getenv("HAYAKU_NATIVE_IMAGE")
	if image == "" {
		t.Skip("installed immutable Linux GCC image required")
	}
	arch := os.Getenv("HAYAKU_NATIVE_ARCH")
	if arch == "" {
		arch = "arm64"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	docker, err := ResolveDocker()
	if err != nil {
		t.Fatal(err)
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := prepareWorker(ctx, goTool, t.TempDir(), arch)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "probe.c"), []byte(dynamicCSource), 0600); err != nil {
		t.Fatal(err)
	}
	libdir := "/lib/aarch64-linux-gnu"
	loader := "ld-linux-aarch64.so.1"
	if arch == "amd64" {
		libdir = "/lib/x86_64-linux-gnu"
		loader = "ld-linux-x86-64.so.2"
	}
	script := "set -eu\n/usr/bin/gcc -O2 -pthread -Wl,-z,noexecstack -o /build/program /build/probe.c\nmkdir /build/libs\ncp -L " + libdir + "/libc.so.6 /build/libs/libc.so.6\ncp -L " + libdir + "/" + loader + " /build/libs/" + loader + "\n"
	out, err := command(ctx, docker, "run", "--rm", "--pull=never", "--network=none", "--mount", "type=bind,src="+directory+",dst=/build", "--entrypoint=/bin/sh", image, "-c", script)
	if err != nil || !out.Completed || out.ExitCode != 0 {
		t.Fatalf("installed C fixture failed: %v %s", err, out.Stderr)
	}
	original, err := command(ctx, docker, "run", "--rm", "--pull=never", "--network=none", "--read-only", "--user=65534:65534", "--mount", "type=bind,src="+filepath.Join(directory, "program")+",dst=/control,readonly", "--entrypoint=/control", image, "original")
	if err != nil || !original.Completed || original.ExitCode != 0 || !strings.Contains(string(original.Stdout), "original-dynamic-positive") {
		t.Fatalf("original dynamic control failed: %v %s", err, original.Stderr)
	}
	resources := filepath.Join(directory, "data-root")
	if err := os.MkdirAll(filepath.Join(resources, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "data", "value"), []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	programData, err := os.ReadFile(filepath.Join(directory, "program"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "code"), programData, 0555); err != nil {
		t.Fatal(err)
	}
	e, err := CaptureEnvelope(ctx, EnvelopeRequest{Files: inputbundle.Request{Roots: []inputbundle.Root{{Name: "resources", Path: resources, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "resources", Path: "."}}}, Env: []string{"DECLARED=fixture"}, ScratchBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	r, err := CaptureRuntime(ctx, RuntimeRequest{Loader: filepath.Join(directory, "libs", loader), Libraries: []RuntimeLibrary{{"libc.so.6", filepath.Join(directory, "libs", "libc.so.6")}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	p, err := CaptureDynamicProgram(ctx, DynamicProgramRequest{Program: filepath.Join(directory, "program"), Args: []string{"inside"}, Runtime: r})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}()
	b := Backend{Docker: docker, Image: image, Worker: worker, Architecture: arch}
	if err := b.Bind(ctx); err != nil {
		t.Fatal(err)
	}
	out, err = RunDynamicProgram(ctx, b, p, e)
	if err != nil || !out.Completed || out.ExitCode != 0 || !strings.Contains(string(out.Stdout), "dynamic-positive-before-denials") || !strings.Contains(string(out.Stdout), "dynamic-thread-and-noexec-positive") {
		t.Fatalf("dynamic operational context failed: %v output=%s stderr=%s", err, out.Stdout, out.Stderr)
	}
	assertNoManagedContainers(t, docker)
}

func TestInstalledLinuxNodeJitlessOperationalProfile(t *testing.T) {
	image, node := os.Getenv("HAYAKU_NATIVE_IMAGE"), os.Getenv("HAYAKU_DYNAMIC_NODE")
	if image == "" || node == "" {
		t.Skip("explicit installed Linux Node and immutable library image required")
	}
	arch := os.Getenv("HAYAKU_NATIVE_ARCH")
	if arch == "" {
		arch = "arm64"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	docker, err := ResolveDocker()
	if err != nil {
		t.Fatal(err)
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := prepareWorker(ctx, goTool, t.TempDir(), arch)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	resources := filepath.Join(root, "resources")
	if err := os.MkdirAll(filepath.Join(resources, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	scripts := map[string]string{
		"main.cjs":   `const fs=require('node:fs/promises');const {Worker}=require('node:worker_threads');const mod=require('./module.cjs');(async()=>{if(process.env.DECLARED!=='fixture'||!process.execArgv.includes('--jitless'))throw Error('context');const data=await fs.readFile(__dirname+'/data/value','utf8');const worker=new Worker(__dirname+'/worker.cjs');const result=await new Promise((resolve,reject)=>{worker.once('message',resolve);worker.once('error',reject)});await worker.terminate();if(data!=='fixture'||result!=='fixture'||mod!==7)throw Error('data');console.log('node-jitless-operational-positive')})().catch(e=>{require('node:fs').writeSync(2,String(e.stack)+'\n');process.exitCode=71})`,
		"module.cjs": "module.exports=7;",
		"worker.cjs": `const fs=require('node:fs');require('node:worker_threads').parentPort.postMessage(fs.readFileSync(__dirname+'/data/value','utf8'));`,
		"data/value": "fixture",
		// This declared configuration contains no provider or include paths.
		// It is part of the modified profile, not the image's default config.
		"openssl.cnf": "# captured Node operational configuration\n",
	}
	for name, text := range scripts {
		if err := os.WriteFile(filepath.Join(resources, name), []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	original, err := command(ctx, docker, "run", "--rm", "--pull=never", "--network=none", "--read-only", "--user=65534:65534", "--mount", "type=bind,src="+node+",dst=/node,readonly", "--mount", "type=bind,src="+resources+",dst=/fixture,readonly", "--entrypoint=/usr/bin/env", image, "-i", "DECLARED=fixture", "/node", "--jitless", "/fixture/main.cjs")
	if err != nil || !original.Completed || original.ExitCode != 0 || !strings.Contains(string(original.Stdout), "node-jitless-operational-positive") {
		t.Fatalf("original jitless Node control failed: %v %s", err, original.Stderr)
	}
	// Node 22.18 documents this explicit startup configuration option:
	// https://nodejs.org/download/release/v22.18.0/docs/api/cli.html#--openssl-configfile
	// Keep the default-config baseline above separate from this modified scope.
	configured, err := command(ctx, docker, "run", "--rm", "--pull=never", "--network=none", "--read-only", "--user=65534:65534", "--mount", "type=bind,src="+node+",dst=/node,readonly", "--mount", "type=bind,src="+resources+",dst=/resources,readonly", "--entrypoint=/usr/bin/env", image, "-i", "DECLARED=fixture", "/node", "--jitless", "--openssl-config=/resources/openssl.cnf", "/resources/main.cjs")
	if err != nil || !configured.Completed || configured.ExitCode != 0 || !strings.Contains(string(configured.Stdout), "node-jitless-operational-positive") {
		t.Fatalf("original explicitly configured Node control failed: %v %s", err, configured.Stderr)
	}
	libraryDir := "/lib/aarch64-linux-gnu"
	loader := "ld-linux-aarch64.so.1"
	if arch == "amd64" {
		libraryDir = "/lib/x86_64-linux-gnu"
		loader = "ld-linux-x86-64.so.2"
	}
	names := []string{"libdl.so.2", "libstdc++.so.6", "libm.so.6", "libgcc_s.so.1", "libpthread.so.0", "libc.so.6"}
	script := "set -eu\nmkdir /build/libs\ncp -L " + libraryDir + "/" + loader + " /build/libs/" + loader + "\n"
	for _, name := range names {
		script += "cp -L " + libraryDir + "/" + name + " /build/libs/" + name + "\n"
	}
	copied, err := command(ctx, docker, "run", "--rm", "--pull=never", "--network=none", "--mount", "type=bind,src="+root+",dst=/build", "--entrypoint=/bin/sh", image, "-c", script)
	if err != nil || !copied.Completed || copied.ExitCode != 0 {
		t.Fatalf("installed Node runtime extraction failed: %v %s", err, copied.Stderr)
	}
	declaration := RuntimeRequest{Loader: filepath.Join(root, "libs", loader)}
	for _, name := range names {
		declaration.Libraries = append(declaration.Libraries, RuntimeLibrary{name, filepath.Join(root, "libs", name)})
	}
	r, err := CaptureRuntime(ctx, declaration)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	p, err := CaptureDynamicProgram(ctx, DynamicProgramRequest{Program: node, Args: []string{"--jitless", "--openssl-config=/resources/openssl.cnf", "/resources/main.cjs"}, Runtime: r})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}()
	if expected := os.Getenv("HAYAKU_DYNAMIC_NODE_SHA256"); expected != "" && p.primary.digest != expected {
		t.Fatal("explicit Node byte identity differs")
	}
	e, err := CaptureEnvelope(ctx, EnvelopeRequest{Files: inputbundle.Request{Roots: []inputbundle.Root{{Name: "resources", Path: resources, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "resources", Path: "."}}}, Env: []string{"DECLARED=fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	b := Backend{Docker: docker, Image: image, Worker: worker, Architecture: arch}
	if err := b.Bind(ctx); err != nil {
		t.Fatal(err)
	}
	out, err := RunDynamicProgram(ctx, b, p, e)
	if err != nil || !out.Completed || out.ExitCode != 0 || !strings.Contains(string(out.Stdout), "node-jitless-operational-positive") {
		t.Fatalf("Node modified operational profile failed: %v output=%s stderr=%s", err, out.Stdout, out.Stderr)
	}
	assertNoManagedContainers(t, docker)
}

func TestDynamicCopiedControlsRejectBytesAndFinalIdentityDrift(t *testing.T) {
	for _, fault := range []string{"bytes", "mode", "replacement", "cancellation"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			first := filepath.Join(dir, "contract.json")
			second := filepath.Join(dir, "boundary")
			for _, name := range []string{first, second} {
				if err := os.WriteFile(name, []byte("original"), 0444); err != nil {
					t.Fatal(err)
				}
			}
			a, err := captureTransportControl(context.Background(), first, digest([]byte("original")), 32)
			if err != nil {
				t.Fatal(err)
			}
			b, err := captureTransportControl(context.Background(), second, digest([]byte("original")), 32)
			if err != nil {
				t.Fatal(err)
			}
			controls := []transportControl{a, b}
			if err := verifyTransportControls(context.Background(), controls); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			switch fault {
			case "bytes":
				if err := os.Chmod(first, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(first, []byte("modified"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(first, 0444); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(first, a.info.ModTime(), a.info.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(first, 0644); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				replacement := filepath.Join(t.TempDir(), "other")
				if err := os.WriteFile(replacement, []byte("original"), 0444); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(replacement, a.info.ModTime(), a.info.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, first); err != nil {
					t.Fatal(err)
				}
			case "cancellation":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if err := verifyTransportControls(ctx, controls); err == nil {
				t.Fatal("copied control fault passed launch gate")
			}
		})
	}
}

// Tracking the natural ReaderAt seam checks bytes actually requested rather
// than reproducing the parser's allocation/loop implementation.
type dynamicTrackingReader struct {
	io.ReaderAt
	largest, total int
	failureOffset  int64
}

func (r *dynamicTrackingReader) ReadAt(data []byte, offset int64) (int, error) {
	if len(data) > r.largest {
		r.largest = len(data)
	}
	r.total += len(data)
	if r.failureOffset != 0 && offset >= r.failureOffset {
		return 0, context.Canceled
	}
	return r.ReaderAt.ReadAt(data, offset)
}
func TestDynamicELFLargeTableReadsBoundedFarEndNames(t *testing.T) {
	data := make([]byte, 2<<20)
	copy(data, dynamicFixtureBytes("/lib/ldfixture.so", "", []string{"libfixture.so"}))
	binary.LittleEndian.PutUint64(data[64+32:], uint64(len(data)))
	binary.LittleEndian.PutUint64(data[64+40:], uint64(len(data)))
	length := uint64(len(data) - 4096)
	nameOffset := uint64((1 << 20) + 7)
	binary.LittleEndian.PutUint64(data[1048:], nameOffset)
	binary.LittleEndian.PutUint64(data[1064:], length)
	copy(data[4096+nameOffset:], "libfixture.so\x00")
	reader := &dynamicTrackingReader{ReaderAt: bytes.NewReader(data)}
	image, err := readDynamicELF(context.Background(), reader, int64(len(data)))
	if err != nil || len(image.needed) != 1 || image.needed[0] != "libfixture.so" {
		t.Fatal("healthy far-end string table", image, err)
	}
	if reader.largest > 256 || reader.total > 8192 {
		t.Fatal("parser read whole or unbounded string table", reader.largest, reader.total)
	}
	for _, fault := range []string{"offset-equal-length", "offset-outside", "nonnul", "long-name", "range-cross-file", "address-overflow", "length-overflow", "name-read-cancel"} {
		t.Run(fault, func(t *testing.T) {
			damaged := append([]byte(nil), data...)
			reader := &dynamicTrackingReader{ReaderAt: bytes.NewReader(damaged)}
			switch fault {
			case "offset-equal-length":
				binary.LittleEndian.PutUint64(damaged[1048:], length)
			case "offset-outside":
				binary.LittleEndian.PutUint64(damaged[1048:], ^uint64(0))
			case "nonnul":
				binary.LittleEndian.PutUint64(damaged[1048:], length-2)
				copy(damaged[len(damaged)-2:], "xx")
			case "long-name":
				for i := 0; i < 256; i++ {
					damaged[4096+nameOffset+uint64(i)] = 'a'
				}
			case "range-cross-file":
				binary.LittleEndian.PutUint64(damaged[1064:], length+1)
			case "address-overflow":
				binary.LittleEndian.PutUint64(damaged[1032:], ^uint64(0)-1)
			case "length-overflow":
				binary.LittleEndian.PutUint64(damaged[1064:], ^uint64(0))
			case "name-read-cancel":
				reader.failureOffset = int64(4096 + nameOffset)
			}
			_, err := readDynamicELF(context.Background(), reader, int64(len(damaged)))
			if err == nil {
				t.Fatal("invalid string table admitted")
			}
			if fault == "name-read-cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("name read cancellation lost", err)
			}
			if reader.largest > 256 || reader.total > 8192 {
				t.Fatal("fault allocated or read table-sized view", reader.largest, reader.total)
			}
		})
	}
}
