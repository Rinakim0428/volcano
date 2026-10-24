package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"
)

//go:embed assets/Book-Text-Studio.html
var assets embed.FS

type discoveryFunction func(context.Context) (Discovery, error)
type previewFunction func(context.Context, ReaderTarget) (BookPreview, error)
type importFunction func(context.Context, ReaderTarget) ([]byte, error)

type allowedBook struct {
	target ReaderTarget
	until  time.Time
}

type bridgeServer struct {
	token    string
	host     string
	discover discoveryFunction
	preview  previewFunction
	importer importFunction
	mu       sync.Mutex
	allowed  map[ReaderTarget]allowedBook
	busy     chan struct{}
	html     []byte
}

func newBridgeServer(token, host string) (*bridgeServer, error) {
	html, err := assets.ReadFile("assets/Book-Text-Studio.html")
	if err != nil {
		return nil, err
	}
	html = bytes.Replace(html, []byte("<head>"), []byte(`<head><meta name="millie-bridge-host" content="1">`), 1)
	return &bridgeServer{token: token, host: host, discover: DiscoverReaders, preview: PreviewBook,
		importer: ImportBook, allowed: make(map[ReaderTarget]allowedBook), busy: make(chan struct{}, 1), html: html}, nil
}

type apiIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func apiError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": apiIssue{Code: code, Message: message}})
}

func readerIssue(err error) apiIssue {
	var readerError *ReaderError
	if errors.As(err, &readerError) {
		return apiIssue{Code: readerError.Code, Message: readerError.Message}
	}
	if errors.Is(err, context.Canceled) {
		return apiIssue{Code: "CANCELLED", Message: "요청을 취소했습니다."}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return apiIssue{Code: "TIMEOUT", Message: "앱의 응답을 기다리는 시간이 초과됐습니다."}
	}
	return apiIssue{Code: "READER_UNAVAILABLE", Message: "현재 리더의 응답을 확인하지 못했습니다."}
}

func (b *bridgeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Host != b.host {
		apiError(w, http.StatusForbidden, "HOST_DENIED", "이 프로그램을 연 로컬 주소에서만 사용할 수 있습니다.")
		return
	}
	if r.URL.Path == "/" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b.html)
		return
	}
	if r.Method != http.MethodPost || (r.URL.Path != "/api/discover" && r.URL.Path != "/api/import") {
		http.NotFound(w, r)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+b.host {
		apiError(w, http.StatusForbidden, "ORIGIN_DENIED", "다른 웹사이트의 연결 요청은 받지 않습니다.")
		return
	}
	expected := "Bearer " + b.token
	if len(r.Header.Get("Authorization")) != len(expected) || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) != 1 {
		apiError(w, http.StatusUnauthorized, "SESSION_REQUIRED", "연결 프로그램이 자동으로 연 창에서 다시 시도하세요.")
		return
	}
	select {
	case b.busy <- struct{}{}:
		defer func() { <-b.busy }()
	default:
		apiError(w, http.StatusConflict, "BUSY", "다른 연결 작업이 진행 중입니다. 완료하거나 취소한 뒤 다시 시도하세요.")
		return
	}
	if r.URL.Path == "/api/discover" {
		b.handleDiscover(w, r)
	} else {
		b.handleImport(w, r)
	}
}

func (b *bridgeServer) handleDiscover(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.allowed = make(map[ReaderTarget]allowedBook)
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	discovery, err := b.discover(ctx)
	if err != nil {
		apiError(w, http.StatusBadGateway, "DISCOVERY_FAILED", "Mac에서 밀리 앱의 현재 리더를 확인하지 못했습니다.")
		return
	}
	books := make([]BookPreview, 0)
	issues := make([]apiIssue, 0)
	deniedPorts := make(map[int]bool)
	if len(discovery.Targets) > 16 {
		discovery.Targets = discovery.Targets[:16]
	}
	for _, target := range discovery.Targets {
		if ctx.Err() != nil {
			break
		}
		if deniedPorts[target.Port] {
			continue
		}
		book, probeError := b.preview(ctx, target)
		if probeError != nil {
			issue := readerIssue(probeError)
			issues = append(issues, issue)
			if issue.Code == "ACCESS_DENIED" {
				deniedPorts[target.Port] = true
			}
			continue
		}
		book.Target = target
		books = append(books, book)
		b.mu.Lock()
		b.allowed[target] = allowedBook{target: target, until: time.Now().Add(2 * time.Minute)}
		b.mu.Unlock()
	}
	if ctx.Err() != nil {
		issues = append(issues, readerIssue(ctx.Err()))
	}
	if len(books) > 0 {
		discovery.State = "readable_books"
	}
	if discovery.Warnings == nil {
		discovery.Warnings = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": discovery.State, "targets": discovery.Targets,
		"warnings": discovery.Warnings, "books": books, "issues": issues})
}

func (b *bridgeServer) handleImport(w http.ResponseWriter, r *http.Request) {
	var target ReaderTarget
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil {
		apiError(w, http.StatusBadRequest, "INVALID_TARGET", "책 연결 정보를 읽을 수 없습니다.")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		apiError(w, http.StatusBadRequest, "INVALID_TARGET", "잘못된 책 연결 요청입니다.")
		return
	}
	b.mu.Lock()
	allowed, ok := b.allowed[target]
	b.mu.Unlock()
	if !ok || time.Now().After(allowed.until) {
		apiError(w, http.StatusConflict, "RECONNECT_REQUIRED", "책 연결을 다시 확인한 뒤 본문 가져오기를 누르세요.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	data, err := b.importer(ctx, target)
	if err != nil {
		issue := readerIssue(err)
		apiError(w, http.StatusBadGateway, issue.Code, issue.Message)
		return
	}
	w.Header().Set("Content-Type", "application/epub+zip")
	w.Header().Set("Content-Disposition", `attachment; filename="reader-text-snapshot.epub"`)
	w.Header().Set("X-Reader-Snapshot", "text-only")
	_, _ = w.Write(data)
}

func main() {
	noOpen := flag.Bool("no-open", false, "Do not open the local browser (development/test only)")
	flag.Parse()
	if runtime.GOOS != "darwin" && !*noOpen {
		fmt.Fprintln(os.Stderr, "이 연결 프로그램은 macOS용입니다.")
		os.Exit(1)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		log.Fatal("연결 세션을 만들지 못했습니다.")
	}
	token := hex.EncodeToString(secret)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		log.Fatal("로컬 연결 창을 준비하지 못했습니다.")
	}
	host := listener.Addr().String()
	app, err := newBridgeServer(token, host)
	if err != nil {
		log.Fatal("포함된 화면 파일을 읽지 못했습니다.")
	}
	server := &http.Server{Handler: app, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Print("로컬 연결이 종료됐습니다.")
		}
	}()
	fmt.Println("밀리 리더 연결 프로그램을 시작했습니다. 연결 창의 버튼을 누르면 현재 열린 책을 확인합니다.")
	fmt.Println("계정 비밀번호나 인증 정보를 수집하지 않습니다. 종료하려면 이 창에서 Ctrl+C를 누르세요.")
	if !*noOpen {
		if err := exec.Command("/usr/bin/open", "http://"+host+"/#millie="+token).Run(); err != nil {
			fmt.Fprintln(os.Stderr, "브라우저를 자동으로 열지 못했습니다. 연결 프로그램을 종료한 뒤 다시 실행하세요.")
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
}
