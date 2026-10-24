package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type discoveryFixtureExit int

func (e discoveryFixtureExit) Error() string { return "fixture command failed" }
func (e discoveryFixtureExit) ExitCode() int { return int(e) }

func TestDiscoveryUnsupportedDoesNotRunCommands(t *testing.T) {
	result, err := discoverReadersForOS(context.Background(), "linux", func(context.Context, string, ...string) ([]byte, []byte, error) {
		t.Fatal("unsupported platform ran a command")
		return nil, nil, nil
	})
	if err != nil || result.State != "unsupported_platform" || len(result.Targets) != 0 {
		t.Fatalf("unexpected result: %+v, %v", result, err)
	}
}

func TestDiscoveryCancellationDoesNotRunCommands(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := discoverReadersForOS(ctx, "darwin", func(context.Context, string, ...string) ([]byte, []byte, error) {
		t.Fatal("canceled discovery ran a command")
		return nil, nil, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestDiscoveryPIDsBoundedAndValidated(t *testing.T) {
	input := "zero -1 0 1 1 2147483648 2\n"
	for i := 3; i <= 20; i++ {
		input += strconv.Itoa(i) + "\n"
	}
	pids, limited := parseMilliePIDs(input)
	if !limited || len(pids) != 16 || pids[0] != 1 || pids[15] != 16 {
		t.Fatalf("PID bounds failed: %v, %v", pids, limited)
	}
}

func TestDiscoveryExecutableCandidates(t *testing.T) {
	input := "p123\nn/Applications/밀리의서재.app/Contents/MacOS/millie\n" +
		"n/Applications/밀리의서재.app/Contents/MacOS/millie\n" +
		"n/Applications/Millie.app/Contents/MacOS/Millie\n" +
		"n/Applications/Bad.app/Contents/../Contents/MacOS/millie\n" +
		"nrelative/Contents/MacOS/millie\n"
	want := []string{"/Applications/밀리의서재.app/Contents/Info.plist"}
	if got := candidateInfoPlists(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDiscoveryListeningPorts(t *testing.T) {
	input := "p123\nf1\ntIPv4\nn127.0.0.1:39001\n" +
		"f2\ntIPv6\nn[::1]:39002\nf3\ntIPv6\nn*:39003\n" +
		"f4\ntIPv4\nn*:39004\nf5\ntIPv4\nn0.0.0.0:39005\n" +
		"f6\ntIPv4\nn192.0.2.4:39006\nf7\ntIPv4\nn127.0.0.1:0\n" +
		"f8\ntIPv4\nn127.0.0.1:65536\nf9\ntIPv4\nn127.0.0.1:39001\n" +
		"f10\ntIPv4\nn127.0.0.1:39007->127.0.0.1:80\n"
	ports, ignoredIPv6, limited := parseListeningPorts(input)
	if !reflect.DeepEqual(ports, []int{39001, 39004, 39005}) || !ignoredIPv6 || limited {
		t.Fatalf("unexpected ports: %v, IPv6=%v, limited=%v", ports, ignoredIPv6, limited)
	}
	ports, _, limited = parseListeningPorts("n127.0.0.1:39001\nn*:39002\nn[::1]:39003\n")
	if !reflect.DeepEqual(ports, []int{39001}) || limited {
		t.Fatalf("untyped wildcard must not imply IPv4: %v", ports)
	}
	var many strings.Builder
	for port := 40000; port < 40020; port++ {
		fmt.Fprintf(&many, "f%d\ntIPv4\nn127.0.0.1:%d\n", port, port)
	}
	ports, _, limited = parseListeningPorts(many.String())
	if len(ports) != 8 || !limited {
		t.Fatalf("port limit failed: %v, %v", ports, limited)
	}
}

func TestDiscoveryURLTargetsAreTypedRedactedAndPrioritized(t *testing.T) {
	urls := "file:///private/app/epub.html?bookSeq=book_A-42&token=secret-one&userId=private-user\n" +
		"http://127.0.0.1:39002/reader?bookSeq=book_B&password=secret-two\n" +
		"http://localhost:39002/reader?bookSeq=book_B\n" +
		"http://[::1]:39001/reader?bookSeq=book_C\n"
	want := []ReaderTarget{
		{Port: 39002, BookSeq: "book_B"},
		{Port: 39001, BookSeq: "book_C"},
		{Port: 39001, BookSeq: "book_A-42"},
		{Port: 39002, BookSeq: "book_A-42"},
	}
	got := targetsForReaderURLs(urls, []int{39001, 39002})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret", "token", "password", "private-user", "file:", "http:"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("output retained private URL material: %s", encoded)
		}
	}
}

func TestDiscoveryRejectsUnverifiedOrUnsafeReaderURLs(t *testing.T) {
	for _, input := range []string{
		"http://127.0.0.1:39002/?bookSeq=one",
		"http://example.com:39001/?bookSeq=one",
		"http://127.0.0.2:39001/?bookSeq=one",
		"http://user:secret@127.0.0.1:39001/?bookSeq=one",
		"http://127.0.0.1:39001/?bookSeq=one#secret",
		"https://127.0.0.1:39001/?bookSeq=one",
		"file://remote.example/reader?bookSeq=one",
		"file:///reader?bookSeq=../secret",
		"file:///reader?bookSeq=%2e%2e%2fsecret",
		"file:///reader?bookSeq=one%00secret",
		"file:///reader?bookSeq=one%0Asecret",
		"file:///reader?bookSeq=one&bookSeq=two",
		"file:///reader?bookSeq=",
		"file:///reader?bookId=one",
		"/Downloads/one/reader.html",
		"file:///reader?bookSeq=" + strings.Repeat("a", 129),
	} {
		if got := targetsForReaderURLs(input, []int{39001}); len(got) != 0 {
			t.Errorf("accepted unsafe or unverified URL %q: %+v", input, got)
		}
	}
	if got := targetsForReaderURLs("file:///reader?bookSeq=one", []int{0, -1, 65536}); len(got) != 0 {
		t.Errorf("accepted invalid ports: %+v", got)
	}
	var many strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&many, "file:///reader?bookSeq=book_%d\n", i)
	}
	if got := targetsForReaderURLs(many.String(), []int{39001, 39002}); len(got) != 16 {
		t.Fatalf("target count not bounded: %d", len(got))
	}
}

type discoveryFixture struct {
	identity    string
	pidError    error
	textError   error
	socketError error
	sockets     string
	urls        string
	scriptError error
	stderr      string
	calls       []string
}

func (f *discoveryFixture) run(t *testing.T) discoveryRunner {
	return func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		t.Helper()
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 20*time.Second {
			t.Fatal("discovery command is missing its bounded deadline")
		}
		f.calls = append(f.calls, name)
		switch name {
		case "/usr/bin/pgrep":
			if !reflect.DeepEqual(args, []string{"-x", "millie"}) {
				t.Fatalf("wrong process discovery: %v", args)
			}
			if f.pidError != nil {
				return nil, []byte(f.stderr), f.pidError
			}
			return []byte("123\n"), nil, nil
		case "/usr/sbin/lsof":
			if reflect.DeepEqual(args, []string{"-a", "-p", "123", "-d", "txt", "-Fn"}) {
				return []byte("p123\nn/Applications/밀리의서재.app/Contents/MacOS/millie\n"), []byte(f.stderr), f.textError
			}
			if !reflect.DeepEqual(args, []string{"-nP", "-a", "-p", "123", "-iTCP", "-sTCP:LISTEN", "-Fnt"}) {
				t.Fatalf("unexpected lsof command: %v", args)
			}
			return []byte(f.sockets), []byte(f.stderr), f.socketError
		case "/usr/libexec/PlistBuddy":
			if !reflect.DeepEqual(args, []string{"-c", "Print :CFBundleIdentifier", "/Applications/밀리의서재.app/Contents/Info.plist"}) {
				t.Fatalf("unexpected bundle verification: %v", args)
			}
			return []byte(f.identity), nil, nil
		case "/usr/bin/osascript":
			if len(args) != 2 || args[0] != "-e" || !strings.Contains(args[1], "unix id is 123") {
				t.Fatalf("Accessibility query did not target verified PID: %v", args)
			}
			return []byte(f.urls), []byte(f.stderr), f.scriptError
		default:
			t.Fatalf("unexpected command: %s", name)
			return nil, nil, errors.New("unexpected command")
		}
	}
}

func TestDiscoveryStateTransitionsAndRedaction(t *testing.T) {
	fixtures := []struct {
		name  string
		state string
		edit  func(*discoveryFixture)
		calls int
	}{
		{"not running", "app_not_running", func(f *discoveryFixture) { f.pidError = discoveryFixtureExit(1) }, 1},
		{"process query failure", "app_unverified", func(f *discoveryFixture) { f.pidError = discoveryFixtureExit(2) }, 1},
		{"wrong bundle ID", "app_unverified", func(f *discoveryFixture) { f.identity = "other.app" }, 3},
		{"wrong process executable", "app_unverified", func(f *discoveryFixture) { f.textError = errors.New("private-error") }, 2},
		{"no socket", "no_local_server", func(f *discoveryFixture) { f.socketError = discoveryFixtureExit(1) }, 4},
		{"only IPv6", "no_local_server", func(f *discoveryFixture) { f.sockets = "p123\nf1\ntIPv6\nn[::1]:39001\n" }, 4},
		{"permission denied", "accessibility_required", func(f *discoveryFixture) {
			f.scriptError = errors.New("private-error")
			f.stderr = "private-secret: not authorized to send Apple events (-1743)"
		}, 5},
		{"unavailable AXURL", "reader_not_found", func(f *discoveryFixture) { f.urls = "" }, 5},
		{"URL read error", "reader_not_found", func(f *discoveryFixture) { f.scriptError = errors.New("private-error") }, 5},
		{"ready is not permission proof", "ready_to_probe", func(f *discoveryFixture) {}, 5},
	}
	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			f := discoveryFixture{
				identity: millieBundleID,
				sockets:  "p123\nf1\ntIPv4\nn127.0.0.1:39001\n",
				urls:     "file:///private/reader?bookSeq=book_123&token=private-secret",
				stderr:   "private-secret",
			}
			tc.edit(&f)
			result, err := discoverReadersForOS(context.Background(), "darwin", f.run(t))
			if err != nil || result.State != tc.state || len(f.calls) != tc.calls {
				t.Fatalf("result=%+v err=%v calls=%v; wanted %s with %d calls", result, err, f.calls, tc.state, tc.calls)
			}
			if tc.state == "ready_to_probe" {
				want := []ReaderTarget{{Port: 39001, BookSeq: "book_123"}}
				if !reflect.DeepEqual(result.Targets, want) {
					t.Fatalf("wrong targets: %+v", result.Targets)
				}
			} else if len(result.Targets) != 0 {
				t.Fatalf("failure state retained targets: %+v", result.Targets)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "file:///") {
				t.Fatalf("private command data leaked: %s", encoded)
			}
		})
	}
}

func TestDiscoveryAXURLScriptReadOnlyBounds(t *testing.T) {
	script := readerAXURLScript(123)
	for _, required := range []string{"unix id is 123", `attribute "AXURL"`, "inspectedCount < 400", "currentDepth < 8"} {
		if !strings.Contains(script, required) {
			t.Errorf("script missing %q", required)
		}
	}
	for _, forbidden := range []string{"AXValue", "AXTitle", "AXDescription", "clipboard", "keystroke", "click ", "activate", "frontmost", "do shell script"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("script contains unrelated data read or mutation: %q", forbidden)
		}
	}
}

func TestDiscoveryKeepsPIDPortsPairedAndGloballyBounded(t *testing.T) {
	var pidList strings.Builder
	for pid := 100; pid < 110; pid++ {
		fmt.Fprintf(&pidList, "%d\n", pid)
	}
	accessibilityCalls := 0
	result, err := discoverReadersForOS(context.Background(), "darwin", func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		switch name {
		case "/usr/bin/pgrep":
			return []byte(pidList.String()), nil, nil
		case "/usr/libexec/PlistBuddy":
			return []byte(millieBundleID), nil, nil
		case "/usr/sbin/lsof":
			for _, arg := range args {
				if arg == "txt" {
					return []byte("n/Applications/Millie.app/Contents/MacOS/millie\n"), nil, nil
				}
			}
			for i, arg := range args {
				if arg == "-p" {
					pid, _ := strconv.Atoi(args[i+1])
					return []byte(fmt.Sprintf("p%d\nf1\ntIPv4\nn127.0.0.1:%d\n", pid, 39000+pid)), nil, nil
				}
			}
		case "/usr/bin/osascript":
			accessibilityCalls++
			for pid := 100; pid < 110; pid++ {
				if strings.Contains(args[1], fmt.Sprintf("unix id is %d\n", pid)) {
					return []byte(fmt.Sprintf("file:///reader?bookSeq=book_%d&token=secret\n", pid)), nil, nil
				}
			}
		}
		t.Fatalf("unexpected fixture command: %s %v", name, args)
		return nil, nil, errors.New("unexpected command")
	})
	if err != nil || result.State != "ready_to_probe" || len(result.Targets) != 8 || accessibilityCalls != 8 {
		t.Fatalf("global bounds failed: %+v err=%v AXCalls=%d", result, err, accessibilityCalls)
	}
	for i, target := range result.Targets {
		pid := 100 + i
		if target.Port != 39000+pid || target.BookSeq != fmt.Sprintf("book_%d", pid) {
			t.Fatalf("cross-process URL/port combination: %+v", target)
		}
	}
}

func TestDiscoveryCommandOutputBound(t *testing.T) {
	var buffer discoveryOutputBuffer
	n, err := buffer.Write([]byte(strings.Repeat("x", 70*1024)))
	if err == nil || n != 64*1024 || buffer.Len() != 64*1024 {
		t.Fatalf("output bound failed: n=%d len=%d err=%v", n, buffer.Len(), err)
	}
	if n, err := buffer.Write([]byte("more")); err == nil || n != 0 {
		t.Fatalf("full buffer accepted extra data: n=%d err=%v", n, err)
	}
}
