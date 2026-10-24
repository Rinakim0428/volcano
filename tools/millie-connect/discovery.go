package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const millieBundleID = "kr.co.millie.MillieShelf"

// Discovery contains no filesystem paths, complete reader URLs, or credentials.
type Discovery struct {
	State    string         `json:"state"`
	Targets  []ReaderTarget `json:"targets"`
	Warnings []string       `json:"warnings"`
}

type discoveryRunner func(context.Context, string, ...string) ([]byte, []byte, error)

// DiscoverReaders reads process identity, listening sockets and AXURL attributes.
// It never launches/quits Millie, changes UI state, reads AXValue/text, or reads a DB.
// Call only after an explicit user action; macOS may request Automation/Accessibility.
func DiscoverReaders(ctx context.Context) (Discovery, error) {
	return discoverReadersForOS(ctx, runtime.GOOS, boundedDiscoveryCommand)
}

func discoverReadersForOS(ctx context.Context, platform string, run discoveryRunner) (Discovery, error) {
	result := Discovery{State: "unsupported_platform", Targets: []ReaderTarget{}, Warnings: []string{}}
	if platform != "darwin" {
		result.Warnings = append(result.Warnings, "밀리 앱 연결은 macOS에서만 확인할 수 있습니다.")
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	pidOutput, _, err := run(ctx, "/usr/bin/pgrep", "-x", "millie")
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil {
		if commandExitCode(err) == 1 && len(bytes.TrimSpace(pidOutput)) == 0 {
			result.State = "app_not_running"
			result.Warnings = append(result.Warnings, "밀리의서재 앱 실행을 찾지 못했습니다. 앱에서 읽을 수 있는 EPUB 책을 열어 주세요.")
		} else {
			result.State = "app_unverified"
			result.Warnings = append(result.Warnings, "앱의 실행 상태를 확인하지 못했습니다. 실행되지 않았다는 뜻은 아닙니다.")
		}
		return result, nil
	}
	pids, pidLimit := parseMilliePIDs(string(pidOutput))
	if len(pids) == 0 {
		result.State = "app_unverified"
		result.Warnings = append(result.Warnings, "실행 목록에서 유효한 밀리 프로세스 식별자를 확인하지 못했습니다.")
		return result, nil
	}
	if pidLimit {
		result.Warnings = append(result.Warnings, "프로세스 확인 한도 16개에 도달해 일부만 확인했습니다.")
	}
	verified, listening, permissionBlocked, inspected := 0, 0, false, false
	seen := make(map[string]bool)
	seenPorts := make(map[int]bool)
	for _, pid := range pids {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		pidText := strconv.Itoa(pid)
		textOutput, _, textErr := run(ctx, "/usr/sbin/lsof", "-a", "-p", pidText, "-d", "txt", "-Fn")
		if textErr != nil {
			continue
		}
		identityOK := false
		for _, plist := range candidateInfoPlists(string(textOutput)) {
			identity, _, identityErr := run(ctx, "/usr/libexec/PlistBuddy", "-c", "Print :CFBundleIdentifier", plist)
			if identityErr == nil && strings.TrimSpace(string(identity)) == millieBundleID {
				identityOK = true
				break
			}
		}
		if !identityOK {
			continue
		}
		verified++
		sockets, _, socketErr := run(ctx, "/usr/sbin/lsof", "-nP", "-a", "-p", pidText, "-iTCP", "-sTCP:LISTEN", "-Fnt")
		if socketErr != nil {
			if commandExitCode(socketErr) != 1 {
				result.Warnings = append(result.Warnings, "확인된 밀리 프로세스의 수신 소켓 일부를 조회하지 못했습니다.")
			}
			continue
		}
		ports, ignoredIPv6, portLimit := parseListeningPorts(string(sockets))
		if ignoredIPv6 {
			result.Warnings = append(result.Warnings, "IPv6 전용 수신 소켓은 현재 IPv4 연결기로 확인하지 않았습니다. 같은 포트를 임의로 다른 주소에 연결하지 않습니다.")
		}
		if portLimit {
			result.Warnings = append(result.Warnings, "수신 포트 확인 한도 8개에 도달해 일부만 확인했습니다.")
		}
		allowedPorts := ports[:0]
		for _, port := range ports {
			if !seenPorts[port] && len(seenPorts) >= 8 {
				result.Warnings = append(result.Warnings, "수신 포트 확인 한도 8개에 도달해 일부만 확인했습니다.")
				continue
			}
			seenPorts[port] = true
			allowedPorts = append(allowedPorts, port)
		}
		ports = allowedPorts
		if len(ports) == 0 {
			continue
		}
		listening++
		urlOutput, scriptError, scriptErr := run(ctx, "/usr/bin/osascript", "-e", readerAXURLScript(pid))
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if scriptErr != nil {
			if accessibilityError(string(scriptError)) {
				permissionBlocked = true
			} else {
				result.Warnings = append(result.Warnings, "리더의 주소 속성을 읽지 못했습니다. 책 본문이나 계정 정보를 읽은 것은 아닙니다.")
			}
			continue
		}
		inspected = true
		for _, candidate := range targetsForReaderURLs(string(urlOutput), ports) {
			key := strconv.Itoa(candidate.Port) + ":" + candidate.BookSeq
			if seen[key] {
				continue
			}
			if len(result.Targets) == 16 {
				result.Warnings = append(result.Warnings, "연결 후보 한도 16개에 도달해 일부만 확인했습니다.")
				break
			}
			seen[key] = true
			result.Targets = append(result.Targets, candidate)
		}
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	switch {
	case len(result.Targets) > 0:
		result.State = "ready_to_probe"
		result.Warnings = append(result.Warnings, "주소 후보만 확인했습니다. 실제 읽기 권한과 본문 응답은 별도 연결 검사에서 확인해야 합니다.")
		if permissionBlocked {
			result.Warnings = append(result.Warnings, "일부 창은 macOS 접근 권한 때문에 확인하지 못했습니다.")
		}
	case verified == 0:
		result.State = "app_unverified"
		result.Warnings = append(result.Warnings, "실행 파일과 번들 ID가 일치하는 밀리 앱을 확인하지 못했습니다.")
	case listening == 0:
		result.State = "no_local_server"
		result.Warnings = append(result.Warnings, "확인된 밀리 앱에서 연결할 수 있는 IPv4 로컬 서버를 찾지 못했습니다.")
	case permissionBlocked:
		result.State = "accessibility_required"
		result.Warnings = append(result.Warnings, "현재 리더 주소를 읽으려면 macOS가 실행 프로그램에 자동화 또는 손쉬운 사용 권한을 허용해야 합니다. 이 프로그램은 권한을 변경하지 않습니다.")
	default:
		result.State = "reader_not_found"
		if inspected {
			result.Warnings = append(result.Warnings, "현재 앱 창에서 bookSeq가 포함된 로컬 리더 주소를 찾지 못했습니다. 다운로드 폴더 이름이나 공유 링크 ID로 대신 추측하지 않습니다.")
		}
	}
	result.Warnings = uniqueWarnings(result.Warnings)
	return result, nil
}

func parseMilliePIDs(output string) ([]int, bool) {
	var pids []int
	seen := make(map[int]bool)
	limited := false
	for _, field := range strings.Fields(output) {
		value, err := strconv.ParseInt(field, 10, 32)
		if err != nil || value <= 0 || seen[int(value)] {
			continue
		}
		if len(pids) == 16 {
			limited = true
			break
		}
		seen[int(value)] = true
		pids = append(pids, int(value))
	}
	return pids, limited
}

func candidateInfoPlists(output string) []string {
	var results []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "n/") {
			continue
		}
		name := strings.TrimPrefix(line, "n")
		if strings.ContainsAny(name, "\r\x00") || !strings.HasSuffix(name, "/Contents/MacOS/millie") || filepath.Clean(name) != name {
			continue
		}
		plist := strings.TrimSuffix(name, "/MacOS/millie") + "/Info.plist"
		if seen[plist] {
			continue
		}
		seen[plist] = true
		results = append(results, plist)
		if len(results) == 4 {
			break
		}
	}
	return results
}

// lsof is asked for both n (address) and t (socket family). IPv6-only sockets
// are not proof that the same port at 127.0.0.1 belongs to the verified process.
func parseListeningPorts(output string) ([]int, bool, bool) {
	ports := []int{}
	seen := make(map[int]bool)
	family := ""
	ignoredIPv6, limited := false, false
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "f") || strings.HasPrefix(line, "p") {
			family = ""
		}
		if strings.HasPrefix(line, "t") {
			family = strings.TrimPrefix(line, "t")
		}
		if !strings.HasPrefix(line, "n") {
			continue
		}
		address := strings.TrimSpace(strings.TrimPrefix(line, "n"))
		address = strings.TrimSuffix(address, " (LISTEN)")
		if strings.Contains(address, "->") {
			continue
		}
		colon := strings.LastIndexByte(address, ':')
		if colon < 0 {
			continue
		}
		host, portText := address[:colon], address[colon+1:]
		if family == "IPv6" || strings.Contains(host, ":") {
			ignoredIPv6 = true
			continue
		}
		if host != "127.0.0.1" && host != "localhost" && host != "0.0.0.0" && !(host == "*" && family == "IPv4") {
			continue
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 || seen[port] {
			continue
		}
		if len(ports) == 8 {
			limited = true
			break
		}
		seen[port] = true
		ports = append(ports, port)
	}
	return ports, ignoredIPv6, limited
}

func discoveryBookSeqValid(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func targetsForReaderURLs(output string, verifiedPorts []int) []ReaderTarget {
	var exactTargets, fileTargets []ReaderTarget
	seen := make(map[string]bool)
	portSet := make(map[int]bool)
	for _, port := range verifiedPorts {
		if port >= 1 && port <= 65535 {
			portSet[port] = true
		}
	}
	for _, line := range strings.Split(output, "\n") {
		if len(line) > 8192 || strings.ContainsAny(line, "\r\x00\t") {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(line))
		if err != nil || u.User != nil || u.Fragment != "" {
			continue
		}
		query, queryErr := url.ParseQuery(u.RawQuery)
		if queryErr != nil || len(query["bookSeq"]) != 1 || !discoveryBookSeqValid(query.Get("bookSeq")) {
			continue
		}
		ports := []int{}
		switch u.Scheme {
		case "file":
			if u.Host != "" && u.Host != "localhost" {
				continue
			}
			ports = verifiedPorts
		case "http":
			host := u.Hostname()
			ip := net.ParseIP(host)
			if host != "localhost" && (ip == nil || !(ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback))) {
				continue
			}
			port := 80
			if u.Port() != "" {
				port, err = strconv.Atoi(u.Port())
			}
			if err != nil || !portSet[port] {
				continue
			}
			ports = []int{port}
		default:
			continue
		}
		for _, port := range ports {
			if !portSet[port] {
				continue
			}
			target := ReaderTarget{Port: port, BookSeq: query.Get("bookSeq")}
			if u.Scheme == "http" {
				exactTargets = append(exactTargets, target)
			} else {
				fileTargets = append(fileTargets, target)
			}
		}
	}
	var targets []ReaderTarget
	// A port explicitly observed in an AXURL takes precedence over combinations
	// needed when a file URL supplies only bookSeq.
	for _, candidate := range append(exactTargets, fileTargets...) {
		key := strconv.Itoa(candidate.Port) + ":" + candidate.BookSeq
		if !seen[key] {
			seen[key] = true
			targets = append(targets, candidate)
			if len(targets) == 16 {
				break
			}
		}
	}
	return targets
}

func readerAXURLScript(pid int) string {
	// Only AXURL and child-element references are read. Missing AXURL is normal.
	// A maximum of 400 nodes and eight levels prevents unbounded UI traversal.
	return fmt.Sprintf(`tell application "System Events"
    set targetProcess to first application process whose unix id is %d
    set pendingNodes to {targetProcess}
    set pendingDepths to {0}
    set foundURLs to {}
    set inspectedCount to 0
    repeat while (count of pendingNodes) > 0 and inspectedCount < 400
        set currentNode to item 1 of pendingNodes
        set currentDepth to item 1 of pendingDepths
        if (count of pendingNodes) = 1 then
            set pendingNodes to {}
            set pendingDepths to {}
        else
            set pendingNodes to items 2 thru -1 of pendingNodes
            set pendingDepths to items 2 thru -1 of pendingDepths
        end if
        set inspectedCount to inspectedCount + 1
        try
            set candidateURL to value of attribute "AXURL" of currentNode
            if candidateURL is not missing value then
                set candidateURL to candidateURL as text
                if candidateURL contains "bookSeq=" then
                    set end of foundURLs to candidateURL
                    if (count of foundURLs) >= 16 then exit repeat
                end if
            end if
        on error errorMessage number errorNumber
            if errorNumber is -1743 or errorNumber is -25211 or errorNumber is 1002 or errorMessage contains "assistive access" or errorMessage contains "손쉬운 사용" or errorMessage contains "보조 접근" then error "ACCESSIBILITY_REQUIRED" number -1743
        end try
        if currentDepth < 8 then
            try
                set childNodes to UI elements of currentNode
                repeat with childNode in childNodes
                    if (count of pendingNodes) + inspectedCount >= 400 then exit repeat
                    set end of pendingNodes to contents of childNode
                    set end of pendingDepths to currentDepth + 1
                end repeat
            on error errorMessage number errorNumber
                if errorNumber is -1743 or errorNumber is -25211 or errorNumber is 1002 or errorMessage contains "assistive access" or errorMessage contains "손쉬운 사용" or errorMessage contains "보조 접근" then error "ACCESSIBILITY_REQUIRED" number -1743
            end try
        end if
    end repeat
    set AppleScript's text item delimiters to linefeed
    return foundURLs as text
end tell`, pid)
}

func accessibilityError(stderr string) bool {
	text := strings.ToLower(stderr)
	for _, marker := range []string{"accessibility_required", "assistive access", "not authorized to send apple events", "not authorised to send apple events", "(-1743)", "(-25211)", "손쉬운 사용", "보조 접근", "보조 액세스", "자동화 접근"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func uniqueWarnings(input []string) []string {
	result := []string{}
	seen := make(map[string]bool)
	for _, value := range input {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func commandExitCode(err error) int {
	var status interface{ ExitCode() int }
	if errors.As(err, &status) {
		return status.ExitCode()
	}
	return -1
}

type discoveryOutputBuffer struct {
	bytes.Buffer
}

func (b *discoveryOutputBuffer) Write(value []byte) (int, error) {
	const maximum = 64 * 1024
	remaining := maximum - b.Len()
	if len(value) > remaining {
		if remaining > 0 {
			_, _ = b.Buffer.Write(value[:remaining])
		}
		return remaining, errors.New("discovery output exceeded limit")
	}
	return b.Buffer.Write(value)
}

func boundedDiscoveryCommand(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.WaitDelay = time.Second
	var stdout, stderr discoveryOutputBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}
