package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testBridge() *bridgeServer {
	return &bridgeServer{
		token: "test-session-secret", host: "127.0.0.1:43210",
		discover: func(context.Context) (Discovery, error) { return Discovery{State: "reader_url_not_found"}, nil },
		preview: func(_ context.Context, target ReaderTarget) (BookPreview, error) {
			return BookPreview{Title: "한글 도서", Chapters: 2, Target: target}, nil
		},
		importer: func(context.Context, ReaderTarget) ([]byte, error) { return []byte("fake-epub-snapshot"), nil },
		allowed:  make(map[ReaderTarget]allowedBook), busy: make(chan struct{}, 1), html: []byte("<!doctype html><p>local interface</p>"),
	}
}

func bridgeRequest(b *bridgeServer, path, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://"+b.host+path, strings.NewReader(body))
	r.Header.Set("Origin", "http://"+b.host)
	r.Header.Set("Authorization", "Bearer "+b.token)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func bridgeErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var value struct {
		Error apiIssue `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("invalid error JSON: %v", err)
	}
	return value.Error.Code
}

func TestBridgeHostOriginAndSessionIsolation(t *testing.T) {
	for _, test := range []struct {
		name, host, origin, auth, code string
		status                         int
	}{
		{"wrong-host", "attacker.invalid:43210", "http://127.0.0.1:43210", "Bearer test-session-secret", "HOST_DENIED", 403},
		{"localhost-alias", "localhost:43210", "http://localhost:43210", "Bearer test-session-secret", "HOST_DENIED", 403},
		{"wrong-port", "127.0.0.1:43211", "http://127.0.0.1:43211", "Bearer test-session-secret", "HOST_DENIED", 403},
		{"external-origin", "127.0.0.1:43210", "https://attacker.invalid", "Bearer test-session-secret", "ORIGIN_DENIED", 403},
		{"null-origin", "127.0.0.1:43210", "null", "Bearer test-session-secret", "ORIGIN_DENIED", 403},
		{"different-scheme", "127.0.0.1:43210", "https://127.0.0.1:43210", "Bearer test-session-secret", "ORIGIN_DENIED", 403},
		{"no-session", "127.0.0.1:43210", "http://127.0.0.1:43210", "", "SESSION_REQUIRED", 401},
		{"wrong-session", "127.0.0.1:43210", "http://127.0.0.1:43210", "Bearer invalid-session", "SESSION_REQUIRED", 401},
		{"same-length-wrong-session", "127.0.0.1:43210", "http://127.0.0.1:43210", "Bearer test-session-secrex", "SESSION_REQUIRED", 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := testBridge()
			called := false
			b.discover = func(context.Context) (Discovery, error) { called = true; return Discovery{}, nil }
			r := bridgeRequest(b, "/api/discover", "")
			r.Host = test.host
			r.Header.Set("Origin", test.origin)
			r.Header.Set("Authorization", test.auth)
			response := httptest.NewRecorder()
			b.ServeHTTP(response, r)
			if response.Code != test.status || bridgeErrorCode(t, response) != test.code || called {
				t.Fatalf("status %d, body %s, discovery called %v", response.Code, response.Body, called)
			}
			if response.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("cross-origin access permitted")
			}
			if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatal("missing isolation headers")
			}
		})
	}
}

func TestBridgePublicHTMLAndMethods(t *testing.T) {
	b := testBridge()
	r := httptest.NewRequest(http.MethodGet, "http://"+b.host+"/", nil)
	response := httptest.NewRecorder()
	b.ServeHTTP(response, r)
	if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), b.html) || strings.Contains(response.Body.String(), b.token) {
		t.Fatal("HTML response or session isolation failed")
	}
	for _, method := range []string{http.MethodGet, http.MethodOptions, http.MethodPut} {
		r = httptest.NewRequest(method, "http://"+b.host+"/api/discover", nil)
		response = httptest.NewRecorder()
		b.ServeHTTP(response, r)
		if response.Code != 404 {
			t.Fatalf("unexpected method allowed: %s", method)
		}
	}
	r = bridgeRequest(b, "/api/discover", "")
	r.Header.Del("Origin")
	response = httptest.NewRecorder()
	b.ServeHTTP(response, r)
	if response.Code != 200 {
		t.Fatal("authenticated local request without Origin was rejected")
	}
}

func TestBridgeDiscoveryBindsOnlyVerifiedSuccessfulTargets(t *testing.T) {
	b := testBridge()
	first := ReaderTarget{Port: 54321, BookSeq: "book-one"}
	refused := ReaderTarget{Port: 54322, BookSeq: "book-two"}
	forged := ReaderTarget{Port: 54323, BookSeq: "not-discovered"}
	b.allowed[forged] = allowedBook{target: forged, until: time.Now().Add(time.Hour)}
	b.discover = func(context.Context) (Discovery, error) {
		return Discovery{State: "reader_targets_found", Targets: []ReaderTarget{first, refused}}, nil
	}
	b.preview = func(_ context.Context, target ReaderTarget) (BookPreview, error) {
		if target == refused {
			return BookPreview{}, readerFailure("ACCESS_DENIED", "request refused", 403)
		}
		return BookPreview{Title: "기기묘묘 부동산", Chapters: 3, Target: forged}, nil
	}
	response := httptest.NewRecorder()
	b.ServeHTTP(response, bridgeRequest(b, "/api/discover", ""))
	if response.Code != 200 {
		t.Fatal(response.Body)
	}
	var value struct {
		State    string        `json:"state"`
		Books    []BookPreview `json:"books"`
		Issues   []apiIssue    `json:"issues"`
		Warnings []string      `json:"warnings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.State != "readable_books" || len(value.Books) != 1 || value.Books[0].Target != first || len(value.Issues) != 1 || value.Issues[0].Code != "ACCESS_DENIED" || value.Warnings == nil {
		t.Fatalf("incorrect discover response: %s", response.Body)
	}
	if len(b.allowed) != 1 || b.allowed[first].target != first || time.Until(b.allowed[first].until) > 2*time.Minute {
		t.Fatalf("incorrect bindings: %#v", b.allowed)
	}
	if _, exists := b.allowed[forged]; exists {
		t.Fatal("stale or forged target retained")
	}
}

func TestBridgeAccessDenialStopsOtherProbesOnSamePort(t *testing.T) {
	b := testBridge()
	denied := ReaderTarget{Port: 54321, BookSeq: "one"}
	skipped := ReaderTarget{Port: 54321, BookSeq: "two"}
	other := ReaderTarget{Port: 54322, BookSeq: "three"}
	b.discover = func(context.Context) (Discovery, error) {
		return Discovery{Targets: []ReaderTarget{denied, skipped, other}}, nil
	}
	probes := []ReaderTarget{}
	b.preview = func(_ context.Context, target ReaderTarget) (BookPreview, error) {
		probes = append(probes, target)
		if target == denied {
			return BookPreview{}, readerFailure("ACCESS_DENIED", "denied", 403)
		}
		return BookPreview{Title: "other book", Chapters: 1}, nil
	}
	response := httptest.NewRecorder()
	b.ServeHTTP(response, bridgeRequest(b, "/api/discover", ""))
	if response.Code != 200 || len(probes) != 2 || probes[0] != denied || probes[1] != other {
		t.Fatalf("wrong probe sequence: %#v", probes)
	}
	if _, exists := b.allowed[skipped]; exists {
		t.Fatal("skipped target authorized")
	}
}

func TestBridgeDiscoveryProbeLimitAndFailureClearsBindings(t *testing.T) {
	b := testBridge()
	targets := make([]ReaderTarget, 20)
	for index := range targets {
		targets[index] = ReaderTarget{Port: 54321 + index, BookSeq: "book"}
	}
	b.discover = func(context.Context) (Discovery, error) { return Discovery{Targets: targets}, nil }
	response := httptest.NewRecorder()
	b.ServeHTTP(response, bridgeRequest(b, "/api/discover", ""))
	if response.Code != 200 || len(b.allowed) != 16 {
		t.Fatalf("probe limit failed: %d targets", len(b.allowed))
	}
	b.discover = func(context.Context) (Discovery, error) { return Discovery{}, errors.New("private diagnostic") }
	response = httptest.NewRecorder()
	b.ServeHTTP(response, bridgeRequest(b, "/api/discover", ""))
	if response.Code != 502 || bridgeErrorCode(t, response) != "DISCOVERY_FAILED" || len(b.allowed) != 0 || strings.Contains(response.Body.String(), "private diagnostic") {
		t.Fatal("failed discovery left stale bindings or exposed error")
	}
}

func TestBridgeImportSuccessAndBoundTarget(t *testing.T) {
	b := testBridge()
	target := ReaderTarget{Port: 54321, BookSeq: "book-one"}
	b.allowed[target] = allowedBook{target: target, until: time.Now().Add(time.Minute)}
	called := false
	b.importer = func(ctx context.Context, actual ReaderTarget) ([]byte, error) {
		called = true
		if actual != target {
			t.Fatalf("wrong importer target: %#v", actual)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("import deadline missing")
		}
		return []byte("fake-epub-snapshot"), nil
	}
	response := httptest.NewRecorder()
	b.ServeHTTP(response, bridgeRequest(b, "/api/import", `{"port":54321,"bookSeq":"book-one"}`))
	if response.Code != 200 || response.Body.String() != "fake-epub-snapshot" || !called {
		t.Fatal("import failed")
	}
	if response.Header().Get("Content-Type") != "application/epub+zip" || response.Header().Get("X-Reader-Snapshot") != "text-only" || response.Header().Get("Content-Disposition") != `attachment; filename="reader-text-snapshot.epub"` {
		t.Fatalf("wrong output headers: %#v", response.Header())
	}
}

func TestBridgeImportRejectsStaleAndUnlistedTargets(t *testing.T) {
	for _, test := range []struct {
		name, body string
		expired    bool
	}{
		{"unlisted-book", `{"port":54321,"bookSeq":"different"}`, false},
		{"unlisted-port", `{"port":54322,"bookSeq":"book-one"}`, false},
		{"expired", `{"port":54321,"bookSeq":"book-one"}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := testBridge()
			target := ReaderTarget{Port: 54321, BookSeq: "book-one"}
			until := time.Now().Add(time.Minute)
			if test.expired {
				until = time.Now().Add(-time.Second)
			}
			b.allowed[target] = allowedBook{target: target, until: until}
			b.importer = func(context.Context, ReaderTarget) ([]byte, error) {
				t.Fatal("importer reached unapproved target")
				return nil, nil
			}
			response := httptest.NewRecorder()
			b.ServeHTTP(response, bridgeRequest(b, "/api/import", test.body))
			if response.Code != 409 || bridgeErrorCode(t, response) != "RECONNECT_REQUIRED" {
				t.Fatalf("target not refused: %s", response.Body)
			}
		})
	}
}

func TestBridgeImportBoundsAndStrictJSON(t *testing.T) {
	for _, body := range []string{
		`{"port":54321,"bookSeq":"one","url":"https://example.invalid"}`,
		`{"port":54321,"bookSeq":"one"} {}`,
		`{"port":54321,"bookSeq":"one"}` + strings.Repeat(" ", 4096),
		`{"port":54321,"bookSeq":"` + strings.Repeat("a", 4096) + `"}`,
		`{"port":"54321","bookSeq":"one"}`,
		`[{"port":54321,"bookSeq":"one"}]`,
		`{"port":54321,`,
	} {
		b := testBridge()
		target := ReaderTarget{Port: 54321, BookSeq: "one"}
		b.allowed[target] = allowedBook{target: target, until: time.Now().Add(time.Minute)}
		b.importer = func(context.Context, ReaderTarget) ([]byte, error) {
			t.Fatal("malformed request reached importer")
			return nil, nil
		}
		response := httptest.NewRecorder()
		b.ServeHTTP(response, bridgeRequest(b, "/api/import", body))
		if response.Code != 400 || bridgeErrorCode(t, response) != "INVALID_TARGET" {
			t.Fatalf("invalid JSON not rejected: status %d body %s", response.Code, response.Body)
		}
	}
}

func TestBridgeBusyAndImportCancellation(t *testing.T) {
	b := testBridge()
	target := ReaderTarget{Port: 54321, BookSeq: "one"}
	b.allowed[target] = allowedBook{target: target, until: time.Now().Add(time.Minute)}
	started := make(chan struct{})
	finished := make(chan struct{})
	var count atomic.Int32
	b.importer = func(ctx context.Context, _ ReaderTarget) ([]byte, error) {
		count.Add(1)
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	response := httptest.NewRecorder()
	r := bridgeRequest(b, "/api/import", `{"port":54321,"bookSeq":"one"}`).WithContext(ctx)
	go func() { defer close(finished); b.ServeHTTP(response, r) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("import did not start")
	}
	busyResponse := httptest.NewRecorder()
	b.ServeHTTP(busyResponse, bridgeRequest(b, "/api/discover", ""))
	if busyResponse.Code != 409 || bridgeErrorCode(t, busyResponse) != "BUSY" {
		t.Fatalf("concurrent request not rejected: %s", busyResponse.Body)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("import cancellation did not propagate")
	}
	if response.Code != 502 || bridgeErrorCode(t, response) != "CANCELLED" || count.Load() != 1 {
		t.Fatalf("cancel response: %s", response.Body)
	}
	response = httptest.NewRecorder()
	b.ServeHTTP(response, bridgeRequest(b, "/api/discover", ""))
	if response.Code != 200 {
		t.Fatal("busy state not released after cancellation")
	}
}

func TestBridgeImportFailureDoesNotReturnPartialArchiveOrDetails(t *testing.T) {
	for _, err := range []error{readerFailure("ACCESS_DENIED", "reader rejected access", 403), errors.New("secret diagnostic")} {
		b := testBridge()
		target := ReaderTarget{Port: 54321, BookSeq: "one"}
		b.allowed[target] = allowedBook{target: target, until: time.Now().Add(time.Minute)}
		b.importer = func(context.Context, ReaderTarget) ([]byte, error) { return []byte("partial archive"), err }
		response := httptest.NewRecorder()
		b.ServeHTTP(response, bridgeRequest(b, "/api/import", `{"port":54321,"bookSeq":"one"}`))
		if response.Code != 502 || strings.Contains(response.Body.String(), "partial archive") || strings.Contains(response.Body.String(), "secret diagnostic") || response.Header().Get("Content-Type") == "application/epub+zip" {
			t.Fatalf("failed import response: %s", response.Body)
		}
	}
}
