package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const fixtureContainer = `<?xml version="1.0" encoding="UTF-8"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`
const fixturePackage = `<package xmlns="http://www.idpf.org/2007/opf"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>기기묘묘 &amp; 부동산</dc:title><dc:creator>작가 한글</dc:creator></metadata><manifest><item id="two" href="text/two.xhtml" media-type="application/xhtml+xml"/><item id="one" href="text/one.xhtml#start" media-type="application/xhtml+xml"/><item id="image" href="https://example.invalid/cover.png" media-type="image/png"/></manifest><spine><itemref idref="one"/><itemref idref="two"/></spine></package>`
const fixtureFirst = `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>1장</title></head><body><h1>첫 번째</h1><p>가나다 &amp; 한글 본문</p><img src="https://example.invalid/not-requested.png"/></body></html>`
const fixtureSecond = `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>2장</title></head><body><p>두 번째 본문</p></body></html>`

type readerFixture struct {
	target     ReaderTarget
	mu         sync.Mutex
	requests   []string
	badHeaders bool
}

func newReaderFixture(t *testing.T, overrides map[string]string) *readerFixture {
	t.Helper()
	fixture := &readerFixture{}
	files := map[string]string{
		"/Book_123/META-INF/container.xml": fixtureContainer,
		"/Book_123/OPS/book.opf":           fixturePackage,
		"/Book_123/OPS/text/one.xhtml":     fixtureFirst,
		"/Book_123/OPS/text/two.xhtml":     fixtureSecond,
	}
	for file, content := range overrides {
		files[file] = content
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		fixture.requests = append(fixture.requests, r.URL.RequestURI())
		for _, header := range []string{"Cookie", "Authorization", "Origin", "Referer"} {
			if r.Header.Get(header) != "" {
				fixture.badHeaders = true
			}
		}
		fixture.mu.Unlock()
		content, exists := files[r.URL.Path]
		if !exists {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
		_, _ = io.WriteString(w, content)
	}))
	t.Cleanup(server.Close)
	fixture.target = targetForServer(t, server)
	return fixture
}

func targetForServer(t *testing.T, server *httptest.Server) ReaderTarget {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return ReaderTarget{Port: port, BookSeq: "Book_123"}
}

func assertReaderCode(t *testing.T, err error, expected string) {
	t.Helper()
	var readerErr *ReaderError
	if !errors.As(err, &readerErr) || readerErr.Code != expected {
		t.Fatalf("expected %s, got %#v", expected, err)
	}
	if readerErr.HTTPStatus < 400 {
		t.Fatalf("error status: %d", readerErr.HTTPStatus)
	}
}

func TestPreviewDoesNotFetchChapters(t *testing.T) {
	f := newReaderFixture(t, nil)
	preview, err := PreviewBook(context.Background(), f.target)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Title != "기기묘묘 & 부동산" || preview.Author != "작가 한글" || preview.Chapters != 2 || preview.Target != f.target {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	want := []string{"/Book_123/META-INF/container.xml", "/Book_123/OPS/book.opf"}
	if !reflect.DeepEqual(f.requests, want) || f.badHeaders {
		t.Fatalf("requests: %#v, forbidden headers: %v", f.requests, f.badHeaders)
	}
}

func TestImportSnapshotPreservesKoreanSpineOrder(t *testing.T) {
	f := newReaderFixture(t, nil)
	data, err := ImportBook(context.Background(), f.target)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 5 || archive.File[0].Name != "mimetype" || archive.File[0].Method != zip.Store {
		t.Fatal("invalid EPUB container")
	}
	entries := map[string]string{}
	for _, entry := range archive.File {
		reader, openErr := entry.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		content, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		entries[entry.Name] = string(content)
	}
	if entries["mimetype"] != "application/epub+zip" || entries["chapter-0001.xhtml"] != fixtureFirst || entries["chapter-0002.xhtml"] != fixtureSecond {
		t.Fatal("snapshot text or reading order changed")
	}
	var pkg packageDocument
	if err := xml.Unmarshal([]byte(entries["book.opf"]), &pkg); err != nil {
		t.Fatal(err)
	}
	if len(pkg.Manifest) != 2 || pkg.Metadata.Titles[0] != "기기묘묘 & 부동산" || pkg.Spine[0].IDRef != "chapter-0001" || pkg.Spine[1].IDRef != "chapter-0002" {
		t.Fatalf("invalid generated OPF: %#v", pkg)
	}
	if strings.Contains(entries["book.opf"], "example.invalid") {
		t.Fatal("non-text resource included in manifest")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	want := []string{"/Book_123/META-INF/container.xml", "/Book_123/OPS/book.opf", "/Book_123/OPS/text/one.xhtml", "/Book_123/OPS/text/two.xhtml"}
	if !reflect.DeepEqual(f.requests, want) || f.badHeaders {
		t.Fatalf("requests: %#v, forbidden headers: %v", f.requests, f.badHeaders)
	}
}

func TestAccessRefusedAndRedirectNeverRetried(t *testing.T) {
	for _, test := range []struct {
		status int
		code   string
	}{{401, "ACCESS_DENIED"}, {403, "ACCESS_DENIED"}, {302, "REDIRECT_BLOCKED"}, {404, "BOOK_NOT_FOUND"}, {500, "READER_HTTP_ERROR"}} {
		t.Run(strconv.Itoa(test.status), func(t *testing.T) {
			var mu sync.Mutex
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests++
				mu.Unlock()
				w.Header().Set("Location", "http://example.invalid/never-fetch")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, "private response must not appear in error")
			}))
			defer server.Close()
			data, err := ImportBook(context.Background(), targetForServer(t, server))
			assertReaderCode(t, err, test.code)
			if data != nil || strings.Contains(err.Error(), "private response") {
				t.Fatal("partial or private data escaped")
			}
			mu.Lock()
			defer mu.Unlock()
			if requests != 1 {
				t.Fatalf("made %d requests", requests)
			}
		})
	}
}

func TestReaderPathsStayInsideBook(t *testing.T) {
	for _, reference := range []string{"https://example.invalid/ch.xhtml", "//example.invalid/ch.xhtml", "/other/book.xhtml", "../../outside.xhtml", "%2e%2e/%2e%2e/outside.xhtml", "%252e%252e/outside.xhtml", "text/ch.xhtml?token=secret", "text/ch.xhtml?", `text\file.xhtml`, "text/%00file.xhtml", "file%3Asecret", "%2Foutside.xhtml", "text/%23hash.xhtml"} {
		t.Run(reference, func(t *testing.T) {
			_, err := resolveReaderPath("OPS/book.opf", reference)
			assertReaderCode(t, err, "UNSAFE_PATH")
		})
	}
	for reference, expected := range map[string]string{"../ch.xhtml": "ch.xhtml", "text/ch.xhtml#page": "OPS/text/ch.xhtml", "text/%ED%95%9C%EA%B8%80.xhtml": "OPS/text/한글.xhtml"} {
		actual, err := resolveReaderPath("OPS/book.opf", reference)
		if err != nil || actual != expected {
			t.Fatalf("%s -> %s, %v", reference, actual, err)
		}
	}
}

func TestUnsafeSpineIsRejectedBeforeChapterRequest(t *testing.T) {
	for _, href := range []string{"https://example.invalid/secret.xhtml", "../../anotherbook/text.xhtml"} {
		f := newReaderFixture(t, map[string]string{"/Book_123/OPS/book.opf": strings.Replace(fixturePackage, "text/one.xhtml#start", href, 1)})
		_, err := ImportBook(context.Background(), f.target)
		assertReaderCode(t, err, "UNSAFE_PATH")
		f.mu.Lock()
		count := len(f.requests)
		f.mu.Unlock()
		if count != 2 {
			t.Fatalf("requested unsafe chapter: %d requests", count)
		}
	}
}

func TestChapterRefusalStopsImmediately(t *testing.T) {
	var mu sync.Mutex
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/Book_123/META-INF/container.xml":
			_, _ = io.WriteString(w, fixtureContainer)
		case "/Book_123/OPS/book.opf":
			_, _ = io.WriteString(w, fixturePackage)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer server.Close()
	data, err := ImportBook(context.Background(), targetForServer(t, server))
	assertReaderCode(t, err, "ACCESS_DENIED")
	if data != nil {
		t.Fatal("returned incomplete snapshot")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 || requests[2] != "/Book_123/OPS/text/one.xhtml" {
		t.Fatalf("requests: %#v", requests)
	}
}

func TestOpaqueAndMalformedChaptersRejected(t *testing.T) {
	for _, test := range []struct{ name, body, code string }{
		{"binary", string([]byte{0, 255, 1}), "UNREADABLE_CONTENT"},
		{"malformed", `<html><body><p>broken</body></html>`, "INVALID_XML"},
		{"no-body", `<html><head><title>error</title></head></html>`, "INVALID_XML"},
		{"multiple-roots", `<html><body/></html><html><body/></html>`, "INVALID_XML"},
		{"doctype", `<!DOCTYPE html SYSTEM "http://example.invalid/private.dtd"><html><body>text</body></html>`, "UNSUPPORTED_XML"},
		{"internal-subset", `<!DOCTYPE html [<!ENTITY secret "private">]><html><body>&secret;</body></html>`, "UNSUPPORTED_XML"},
		{"public-dtd", `<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN" "http://example.invalid/xhtml11.dtd"><html><body>text</body></html>`, "UNSUPPORTED_XML"},
		{"entity", `<html><body>&privateEntity;</body></html>`, "INVALID_XML"},
		{"nonutf8-declaration", `<?xml version="1.0" encoding="ISO-8859-1"?><html><body>text</body></html>`, "INVALID_XML"},
		{"json", `{"error":"not a document"}`, "INVALID_XML"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newReaderFixture(t, map[string]string{"/Book_123/OPS/text/one.xhtml": test.body})
			data, err := ImportBook(context.Background(), f.target)
			assertReaderCode(t, err, test.code)
			if data != nil {
				t.Fatal("returned rejected chapter")
			}
			f.mu.Lock()
			count := len(f.requests)
			f.mu.Unlock()
			if count != 3 {
				t.Fatalf("continued fetching after rejection: %d", count)
			}
		})
	}
}

func TestPlainHTMLDoctypeIsSupported(t *testing.T) {
	f := newReaderFixture(t, map[string]string{"/Book_123/OPS/text/one.xhtml": "<!DOCTYPE html>" + fixtureFirst})
	data, err := ImportBook(context.Background(), f.target)
	if err != nil || len(data) == 0 {
		t.Fatalf("plain XHTML doctype rejected: %v", err)
	}
	for _, content := range []string{
		"<!DOCTYPE html><!DOCTYPE html>" + fixtureFirst,
		fixtureFirst + "<!DOCTYPE html>",
		"<!DOCTYPE html []>" + fixtureFirst,
	} {
		assertReaderCode(t, validateReaderXML([]byte(content), "html", true), "UNSUPPORTED_XML")
	}
}

func TestSizeBounds(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprint("chunked=", chunked), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if chunked {
					w.(http.Flusher).Flush()
				} else {
					w.Header().Set("Content-Length", strconv.Itoa(readerMetadataLimit+1))
				}
				_, _ = io.WriteString(w, strings.Repeat(" ", readerMetadataLimit+1))
			}))
			defer server.Close()
			_, err := PreviewBook(context.Background(), targetForServer(t, server))
			assertReaderCode(t, err, "SIZE_LIMIT")
		})
	}
	f := newReaderFixture(t, nil)
	session, err := newReaderSession(f.target)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	session.readBytes = readerTotalLimit - 1
	_, err = session.read(context.Background(), "META-INF/container.xml", readerMetadataLimit)
	assertReaderCode(t, err, "SIZE_LIMIT")
}

func TestMetadataAndSpineValidation(t *testing.T) {
	for _, test := range []struct{ name, opf, code string }{
		{"empty-spine", `<package><metadata/><manifest/><spine/></package>`, "SPINE_LIMIT"},
		{"oversize-spine", `<package><manifest/><spine>` + strings.Repeat(`<itemref idref="x"/>`, readerSpineLimit+1) + `</spine></package>`, "SPINE_LIMIT"},
		{"missing-reference", strings.Replace(fixturePackage, `idref="one"`, `idref="missing"`, 1), "UNSUPPORTED_CHAPTER"},
		{"duplicate-id", strings.Replace(fixturePackage, `id="two"`, `id="one"`, 1), "INVALID_MANIFEST"},
		{"protected", strings.Replace(fixturePackage, "text/one.xhtml#start", "text/one.ncg", 1), "PROTECTED_CONTENT"},
		{"malformed", `<package>`, "INVALID_XML"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newReaderFixture(t, map[string]string{"/Book_123/OPS/book.opf": test.opf})
			_, err := PreviewBook(context.Background(), f.target)
			assertReaderCode(t, err, test.code)
		})
	}
}

func TestInvalidTargetsAndCancellation(t *testing.T) {
	for _, target := range []ReaderTarget{{Port: 0, BookSeq: "123"}, {Port: 65536, BookSeq: "123"}, {Port: 1234, BookSeq: "../x"}, {Port: 1234, BookSeq: "한글"}, {Port: 1234, BookSeq: strings.Repeat("a", 129)}} {
		_, err := PreviewBook(context.Background(), target)
		assertReaderCode(t, err, "INVALID_TARGET")
	}
	f := newReaderFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ImportBook(ctx, f.target)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 0 {
		t.Fatal("request made after cancellation")
	}
}
