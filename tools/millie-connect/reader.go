package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	readerMetadataLimit = 2 << 20
	readerChapterLimit  = 24 << 20
	readerTotalLimit    = 128 << 20
	readerSpineLimit    = 2000
)

// ReaderTarget contains only non-secret values observed in the running reader.
// The caller must authorize this pair against the discovered Millie process.
type ReaderTarget struct {
	Port    int    `json:"port"`
	BookSeq string `json:"bookSeq"`
}

type BookPreview struct {
	Title    string       `json:"title"`
	Author   string       `json:"author"`
	Chapters int          `json:"chapters"`
	Target   ReaderTarget `json:"target"`
}

// ReaderError deliberately contains no response body, credentials or raw URL.
type ReaderError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"-"`
}

func (e *ReaderError) Error() string { return e.Message }

func readerFailure(code, message string, status int) error {
	return &ReaderError{Code: code, Message: message, HTTPStatus: status}
}

var readerBookIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func validateReaderTarget(target ReaderTarget) error {
	if target.Port < 1 || target.Port > 65535 || !readerBookIDPattern.MatchString(target.BookSeq) {
		return readerFailure("INVALID_TARGET", "현재 리더의 도서 주소 또는 로컬 포트가 올바르지 않습니다.", http.StatusBadRequest)
	}
	return nil
}

type readerSession struct {
	target    ReaderTarget
	client    *http.Client
	transport *http.Transport
	readBytes int64
}

func newReaderSession(target ReaderTarget) (*readerSession, error) {
	if err := validateReaderTarget(target); err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy: nil, // These requests are strictly local and never use a proxy.
		DialContext: (&net.Dialer{
			Timeout: 3 * time.Second,
		}).DialContext,
		DisableCompression:  true,
		MaxConnsPerHost:     2,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     10 * time.Second,
	}
	return &readerSession{
		target:    target,
		transport: transport,
		client: &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (s *readerSession) close() { s.transport.CloseIdleConnections() }

func (s *readerSession) read(ctx context.Context, relativePath string, maximum int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maximum > readerTotalLimit-s.readBytes {
		maximum = readerTotalLimit - s.readBytes
	}
	if maximum <= 0 {
		return nil, readerFailure("SIZE_LIMIT", "리더가 제공한 본문이 128 MiB 처리 한도를 넘습니다.", http.StatusUnprocessableEntity)
	}
	requestURL := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("127.0.0.1", fmt.Sprint(s.target.Port)),
		Path:   "/" + s.target.BookSeq + "/" + relativePath,
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, readerFailure("INVALID_TARGET", "리더 요청을 만들 수 없습니다.", http.StatusBadRequest)
	}
	// No cookies, authorization, license data, Origin or Referer are supplied.
	response, err := s.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			return nil, readerFailure("READER_TIMEOUT", "밀리 로컬 서버의 응답 시간이 초과됐습니다.", http.StatusGatewayTimeout)
		}
		return nil, readerFailure("READER_UNREACHABLE", "밀리 로컬 서버에 연결하지 못했습니다. 앱에서 책이 열려 있는지 확인하세요.", http.StatusBadGateway)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, readerFailure("ACCESS_DENIED", "밀리 앱이 외부 연결의 본문 요청을 거부했습니다. 인증·참조 주소를 위조하거나 보호를 우회하지 않습니다.", http.StatusForbidden)
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, readerFailure("REDIRECT_BLOCKED", "본문 요청이 다른 주소로 이동하도록 응답하여 중단했습니다.", http.StatusBadGateway)
	}
	if response.StatusCode == http.StatusNotFound {
		return nil, readerFailure("BOOK_NOT_FOUND", "현재 로컬 서버에서 이 리더의 본문 주소를 찾지 못했습니다.", http.StatusNotFound)
	}
	if response.StatusCode != http.StatusOK {
		return nil, readerFailure("READER_HTTP_ERROR", "밀리 로컬 서버가 본문을 정상적으로 제공하지 않았습니다.", http.StatusBadGateway)
	}
	if response.ContentLength > maximum {
		return nil, readerFailure("SIZE_LIMIT", "리더 자료가 처리 가능한 크기를 넘습니다.", http.StatusUnprocessableEntity)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, readerFailure("READ_FAILED", "밀리 로컬 서버의 자료를 끝까지 읽지 못했습니다.", http.StatusBadGateway)
	}
	if int64(len(data)) > maximum {
		return nil, readerFailure("SIZE_LIMIT", "리더 자료가 처리 가능한 크기를 넘습니다.", http.StatusUnprocessableEntity)
	}
	s.readBytes += int64(len(data))
	return data, nil
}

// resolveReaderPath resolves an OPF resource entirely inside the current book.
// Percent escapes are decoded once, then re-encoded by net/url when requested.
func resolveReaderPath(baseFile, reference string) (string, error) {
	bad := func() (string, error) {
		return "", readerFailure("UNSAFE_PATH", "리더 본문 주소가 도서 범위를 벗어나거나 지원하지 않는 주소입니다.", http.StatusUnprocessableEntity)
	}
	if reference == "" {
		return bad()
	}
	parsed, err := url.Parse(reference)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery {
		return bad()
	}
	decoded := parsed.Path // net/url decodes Path exactly once.
	if decoded == "" || strings.HasPrefix(decoded, "/") || strings.ContainsAny(decoded, `\:%?#`) || !utf8.ValidString(decoded) {
		return bad()
	}
	for _, character := range decoded {
		if unicode.IsControl(character) {
			return bad()
		}
	}
	parts := []string{}
	if baseFile != "" && path.Dir(baseFile) != "." {
		parts = strings.Split(path.Dir(baseFile), "/")
	}
	for _, part := range strings.Split(decoded, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) == 0 {
				return bad()
			}
			parts = parts[:len(parts)-1]
		default:
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return bad()
	}
	return strings.Join(parts, "/"), nil
}

func validateReaderXML(data []byte, rootName string, requireBody bool) error {
	if len(data) == 0 || !utf8.Valid(data) {
		return readerFailure("UNREADABLE_CONTENT", "앱이 제공한 자료를 UTF-8 XML 본문으로 읽을 수 없습니다. 암호화되었거나 다른 형식일 수 있습니다.", http.StatusUnprocessableEntity)
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	seenRoot, seenBody, seenDoctype, depth := false, false, false, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return readerFailure("INVALID_XML", "앱이 제공한 본문 문서의 XML 구조를 확인하지 못했습니다.", http.StatusUnprocessableEntity)
		}
		switch value := token.(type) {
		case xml.Directive:
			// A plain HTML declaration carries no DTD, entity definitions or URL.
			// EPUB XHTML commonly includes it; all other declarations stay blocked.
			fields := strings.Fields(string(value))
			if rootName == "html" && !seenRoot && !seenDoctype && len(fields) == 2 && fields[0] == "DOCTYPE" && fields[1] == "html" {
				seenDoctype = true
				continue
			}
			return readerFailure("UNSUPPORTED_XML", "외부 DTD 또는 사용자 정의 엔티티가 있는 리더 자료는 현재 지원하지 않습니다.", http.StatusUnprocessableEntity)
		case xml.StartElement:
			if !seenRoot {
				if value.Name.Local != rootName {
					return readerFailure("INVALID_XML", "앱 응답이 예상한 EPUB 본문 형식이 아닙니다.", http.StatusUnprocessableEntity)
				}
				seenRoot = true
			} else if depth == 0 {
				return readerFailure("INVALID_XML", "리더 XML 문서에 여러 최상위 요소가 있습니다.", http.StatusUnprocessableEntity)
			}
			depth++
			if depth > 512 {
				return readerFailure("XML_DEPTH_LIMIT", "리더 XML 문서의 중첩이 처리 한도를 넘습니다.", http.StatusUnprocessableEntity)
			}
			if value.Name.Local == "body" {
				seenBody = true
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(value)) != "" && !bytes.Equal(value, []byte{0xef, 0xbb, 0xbf}) {
				return readerFailure("INVALID_XML", "리더 XML 문서 밖에 예상하지 않은 자료가 있습니다.", http.StatusUnprocessableEntity)
			}
		}
	}
	if !seenRoot || depth != 0 || requireBody && !seenBody {
		return readerFailure("INVALID_XML", "리더 문서에 필요한 본문 구조가 없습니다.", http.StatusUnprocessableEntity)
	}
	return nil
}

type containerDocument struct {
	RootFiles []struct {
		FullPath  string `xml:"full-path,attr"`
		MediaType string `xml:"media-type,attr"`
	} `xml:"rootfiles>rootfile"`
}

type manifestItem struct {
	ID        string `xml:"id,attr"`
	Href      string `xml:"href,attr"`
	MediaType string `xml:"media-type,attr"`
}

type packageDocument struct {
	Metadata struct {
		Titles  []string `xml:"title"`
		Authors []string `xml:"creator"`
	} `xml:"metadata"`
	Manifest []manifestItem `xml:"manifest>item"`
	Spine    []struct {
		IDRef string `xml:"idref,attr"`
	} `xml:"spine>itemref"`
}

type readerBook struct {
	preview BookPreview
	paths   []string
}

func cleanReaderMetadata(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 2000 {
		return string(runes[:2000])
	}
	return value
}

func (s *readerSession) loadBook(ctx context.Context) (readerBook, error) {
	var result readerBook
	containerBytes, err := s.read(ctx, "META-INF/container.xml", readerMetadataLimit)
	if err != nil {
		return result, err
	}
	if err = validateReaderXML(containerBytes, "container", false); err != nil {
		return result, err
	}
	var container containerDocument
	if xml.Unmarshal(containerBytes, &container) != nil {
		return result, readerFailure("INVALID_XML", "리더의 EPUB 위치 정보를 읽을 수 없습니다.", http.StatusUnprocessableEntity)
	}
	var packageReference string
	for _, item := range container.RootFiles {
		if item.MediaType == "application/oebps-package+xml" {
			packageReference = item.FullPath
			break
		}
	}
	packagePath, err := resolveReaderPath("", packageReference)
	if err != nil {
		return result, err
	}
	packageBytes, err := s.read(ctx, packagePath, readerMetadataLimit)
	if err != nil {
		return result, err
	}
	if err = validateReaderXML(packageBytes, "package", false); err != nil {
		return result, err
	}
	var document packageDocument
	if xml.Unmarshal(packageBytes, &document) != nil {
		return result, readerFailure("INVALID_XML", "리더의 EPUB 목차를 읽을 수 없습니다.", http.StatusUnprocessableEntity)
	}
	if len(document.Spine) == 0 || len(document.Spine) > readerSpineLimit {
		return result, readerFailure("SPINE_LIMIT", "리더의 읽기 순서가 없거나 본문 항목이 2,000개를 넘습니다.", http.StatusUnprocessableEntity)
	}
	manifest := make(map[string]manifestItem, len(document.Manifest))
	for _, item := range document.Manifest {
		if item.ID == "" {
			return result, readerFailure("INVALID_MANIFEST", "리더의 본문 목록에 ID가 없는 항목이 있습니다.", http.StatusUnprocessableEntity)
		}
		if _, exists := manifest[item.ID]; exists {
			return result, readerFailure("INVALID_MANIFEST", "리더의 본문 목록에 중복된 ID가 있습니다.", http.StatusUnprocessableEntity)
		}
		manifest[item.ID] = item
	}
	for _, spine := range document.Spine {
		item, exists := manifest[spine.IDRef]
		if !exists || item.MediaType != "application/xhtml+xml" && item.MediaType != "text/html" {
			return result, readerFailure("UNSUPPORTED_CHAPTER", "리더의 본문을 XHTML 형식으로 구성할 수 없습니다.", http.StatusUnprocessableEntity)
		}
		chapterPath, resolveErr := resolveReaderPath(packagePath, item.Href)
		if resolveErr != nil {
			return result, resolveErr
		}
		if strings.HasSuffix(strings.ToLower(chapterPath), ".ncg") {
			return result, readerFailure("PROTECTED_CONTENT", "리더가 보호된 파일 주소를 제공했습니다. 이 연결기는 보호를 해제하지 않습니다.", http.StatusUnprocessableEntity)
		}
		result.paths = append(result.paths, chapterPath)
	}
	title := "밀리 리더 도서"
	if len(document.Metadata.Titles) > 0 && cleanReaderMetadata(document.Metadata.Titles[0]) != "" {
		title = cleanReaderMetadata(document.Metadata.Titles[0])
	}
	authors := []string{}
	for _, value := range document.Metadata.Authors {
		if author := cleanReaderMetadata(value); author != "" {
			authors = append(authors, author)
		}
	}
	result.preview = BookPreview{Title: title, Author: cleanReaderMetadata(strings.Join(authors, ", ")), Chapters: len(result.paths), Target: s.target}
	return result, nil
}

// PreviewBook requests only container.xml and the OPF, never chapter bodies.
func PreviewBook(ctx context.Context, target ReaderTarget) (BookPreview, error) {
	session, err := newReaderSession(target)
	if err != nil {
		return BookPreview{}, err
	}
	defer session.close()
	book, err := session.loadBook(ctx)
	return book.preview, err
}

func xmlText(value string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(value))
	return escaped.String()
}

func snapshotOPF(preview BookPreview) string {
	var out strings.Builder
	out.WriteString(`<?xml version="1.0" encoding="UTF-8"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="snapshot-id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="snapshot-id">millie-reader-text-snapshot</dc:identifier><dc:title>`)
	out.WriteString(xmlText(preview.Title))
	out.WriteString(`</dc:title><dc:creator>`)
	out.WriteString(xmlText(preview.Author))
	out.WriteString(`</dc:creator><dc:language>ko</dc:language><dc:description>현재 리더가 제공한 텍스트 문서만 수집한 스냅샷입니다. 원본 도서 파일이나 전체 도서임을 보장하지 않습니다. 이미지·글꼴·미디어는 포함하지 않습니다.</dc:description></metadata><manifest>`)
	for index := 1; index <= preview.Chapters; index++ {
		fmt.Fprintf(&out, `<item id="chapter-%04d" href="chapter-%04d.xhtml" media-type="application/xhtml+xml"/>`, index, index)
	}
	out.WriteString(`</manifest><spine>`)
	for index := 1; index <= preview.Chapters; index++ {
		fmt.Fprintf(&out, `<itemref idref="chapter-%04d"/>`, index)
	}
	out.WriteString(`</spine></package>`)
	return out.String()
}

// ImportBook collects only readable XHTML offered by the active local server.
// It does not decrypt data, reproduce the original EPUB archive, or prove that
// the reader has supplied the whole book (the supplied book may be a preview).
func ImportBook(ctx context.Context, target ReaderTarget) ([]byte, error) {
	session, err := newReaderSession(target)
	if err != nil {
		return nil, err
	}
	defer session.close()
	book, err := session.loadBook(ctx)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	writeEntry := func(name string, data []byte, method uint16) error {
		entry, writeErr := archive.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if writeErr == nil {
			_, writeErr = entry.Write(data)
		}
		return writeErr
	}
	if err = writeEntry("mimetype", []byte("application/epub+zip"), zip.Store); err != nil {
		return nil, err
	}
	container := `<?xml version="1.0" encoding="UTF-8"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`
	if err = writeEntry("META-INF/container.xml", []byte(container), zip.Deflate); err != nil {
		return nil, err
	}
	if err = writeEntry("book.opf", []byte(snapshotOPF(book.preview)), zip.Deflate); err != nil {
		return nil, err
	}
	for index, chapterPath := range book.paths {
		data, readErr := session.read(ctx, chapterPath, readerChapterLimit)
		if readErr != nil {
			return nil, readErr
		}
		if err = validateReaderXML(data, "html", true); err != nil {
			return nil, err
		}
		if err = writeEntry(fmt.Sprintf("chapter-%04d.xhtml", index+1), data, zip.Deflate); err != nil {
			return nil, err
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = archive.Close(); err != nil {
		return nil, readerFailure("SNAPSHOT_FAILED", "가져온 본문을 변환기에 전달할 파일로 만들지 못했습니다.", http.StatusInternalServerError)
	}
	return output.Bytes(), nil
}
