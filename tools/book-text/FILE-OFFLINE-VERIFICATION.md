# Book-Text-Studio 배포 ZIP 독립 실행 검증

확인일: 2026-10-25

## 결론

정적 검사와 재빌드 일치 검사는 통과했습니다. 이 실행 환경에서는 Chromium이 시작 직후 종료되어 `file://` HTML을 열지 못했습니다. 본문 추출, 실제 PDF 워커 실행, 다운로드, 실행 중 외부 요청 유무를 확인했다는 결론은 내릴 수 없습니다. 앱의 실행상 결함은 확인되지 않았으며, 변환기 코드와 배포 ZIP은 변경하지 않았습니다.

기존 localhost 테스트를 확장하고 배포 ZIP을 직접 검사하는 진입점을 추가했습니다. 브라우저 테스트는 실제 실행 가능한 환경에서 완료해야 합니다. 정적 검사 통과는 브라우저 테스트 통과를 대신하지 않습니다.

## 검사 대상

- 저장소: `Rinakim0428/volcano`
- 기준 브랜치: `codex/book-text-export-20261024`
- 기준 커밋: `e9f0eb7baa0f5bef66b5bd099902b67529563298`
- 배포 ZIP: `downloads/Book-Text-Studio.zip` (2,313,534 bytes)
- ZIP SHA256: `6f3b8a42efb29fe09e0d19f1372a3df97b309701cb4ca75d7e4fe99267e39aee`
- ZIP 내부 HTML SHA256: `80b2bae8932a11b574c05a20ed8805a47a6233adcbdef6f35f78afb273debb6a`
- 검사 범위: 사용자가 선택한 EPUB·PDF·TXT를 처리하는 독립 실행형 변환기

실험용 Mac 연결기 파일과 배포물은 변경하지 않았으며, 어떤 브랜치도 병합하지 않았습니다. GitHub Pages를 배포하지 않았습니다.

## 실제 수행 결과

| 항목 | 결과 | 근거와 한계 |
| --- | --- | --- |
| ZIP SHA256·CRC·파일 목록 | 통과 | 체크섬과 4개 멤버 확인: HTML, 사용방법, 직접 작성 예제 EPUB, 라이선스 |
| ZIP과 저장소 배포 파일 일치 | 통과 | ZIP HTML = `downloads/Book-Text-Studio.html` = `index.html`; 안내·라이선스도 일치 |
| 잠금 파일 기준 설치와 재빌드 | 통과 | `npm ci`, `npm run build`; 생성 HTML 4,692,255 bytes, 기존 파일과 동일 |
| 내장 PDF 자료 | 통과(정적) | 워커 1,321,307 UTF-8 bytes, CMap 168개, 표준 글꼴 14개; base64 유효성과 한글 CMap 존재 확인 |
| 애플리케이션·PDF 워커 구문 | 통과(정적) | ZIP에서 추출한 스크립트를 Node 구문 검사로 확인. 실행 검사는 아님 |
| HTML의 외부 리소스 속성 | 발견되지 않음(정적) | HTML 파서 검사. 동적 요청이 없다는 증거는 아님 |
| 신규 Python 테스트 구문 | 통과 | 두 테스트 스크립트 `py_compile` |
| 정적 검사 오류 탐지 | 통과 | 체크섬 오류, 예제 누락, 외부 스크립트, 워커 누락, 워커 구문 오류를 주입한 5가지 대조 실험에서 모두 거부 |
| `file://` HTML 직접 실행 | 미검증 | Chromium 시작 실패로 `page.goto()`에 도달하지 못함 |
| EPUB·PDF·TXT 본문 추출 | 미검증(`file://`) | 기존 localhost 검증과 구분 |
| 실제 PDF 워커·CMap 실행 | 미검증(`file://`) | 내장 파일의 존재와 실행 성공은 별개 |
| TXT·Markdown·JSON 다운로드 | 미검증(`file://`) | 다운로드 파일 생성·내용 확인 단계에 도달하지 못함 |
| 실행 중 외부 네트워크 요청 | 미검증 | 애플리케이션을 열지 못했으므로 0건이라고 보고하지 않음 |
| macOS/Windows의 일반 Chrome | 미검증 | 해당 운영체제 및 사용자 브라우저에서 실행하지 않음 |

## 브라우저 실행을 막은 환경 문제

기본 환경에는 Chromium 실행 파일이 없었습니다. Playwright 설치 경로는 브라우저 ZIP 대신 195-byte `Site Unavailable` HTML을 반환했습니다. 별도로 npm의 `@sparticuz/chromium@153.0.0`에 포함된 실행 파일을 준비하여 `Chromium 153.0.8010.0` 버전 응답을 확인했습니다.

그러나 Python Playwright 1.64.1의 브라우저 시작 단계가 `TargetClosedError`로 실패했고, 프로세스 로그에는 `signal=SIGTRAP`이 기록되었습니다. Node Playwright와 Chromium의 직접 `about:blank` 실행도 같은 단계에서 종료되었습니다. 라이브러리 연결 검사에서는 누락된 공유 라이브러리가 없었습니다. 정확한 종료 원인은 이 환경에서 확정하지 못했습니다. 이는 HTML을 열기 전의 실패이므로 변환기 결함으로 분류하지 않습니다.

파일 접근이나 출처 검사를 완화하는 브라우저 옵션으로 테스트를 우회하지 않았습니다. 단일 프로세스·GPU 비활성화 시작 시도도 실패했으며, 해당 옵션은 회귀 테스트에 추가하지 않았습니다.

## 구현 정적 검토

- `pdf-bootstrap.js`는 포함된 워커 문자열로 Blob URL을 만들고 PDF.js에 전달합니다. PDF.js는 `file://`의 불투명 출처에서 추가 Blob 모듈을 만들어 그 워커를 import할 수 있도록 구성되어 있습니다. 이 경로의 실제 성공 여부는 브라우저 검증이 필요합니다.
- PDF의 `BinaryDataFactory`는 포함된 base64 자료를 읽으며 `useWorkerFetch: false`를 사용합니다. 워커·CMap·표준 글꼴을 CDN에서 가져오도록 설정하지 않았습니다.
- EPUB은 사용자가 선택한 File을 읽고 ZIP 워커를 끈 상태에서 압축을 풉니다. EPUB 문서는 XML로 파싱하고 활성 DOM에 연결하지 않습니다. 외부 이미지 URL과 스크립트가 포함된 합성 EPUB을 기존 테스트에서 재사용합니다.
- TXT는 File의 바이트를 디코딩합니다. 다운로드는 Blob URL과 `download` 속성으로 구현되어 있습니다.
- Mac 연결 초기화는 `http:`와 `127.0.0.1` 조건을 만족하지 않으면 즉시 반환하므로 `file://`에서는 연결 요청 경로로 진입하지 않습니다. 연결기 자체의 동작은 이번 검사 대상이 아닙니다.

위 내용은 구현 검토이며, 실행 중 네트워크 요청 0건이나 오프라인 변환 성공을 증명하지 않습니다.

## 추가한 회귀 검사

`test-release.py`는 소스를 재빌드하거나 HTTP 서버를 열지 않고 실제 ZIP을 검사합니다. SHA256·CRC·허용된 멤버 목록을 확인하고, 한글·공백·`#`가 있는 임시 폴더에 추출합니다. 디스크 보조 파일에 기대지 않도록 배포 폴더에는 ZIP의 4개 파일만 둡니다. 기본 ZIP은 저장소 HTML 및 안내·라이선스와도 비교합니다.

이어 `test-browser.py --transport file`을 새 브라우저에서 두 번 실행합니다.

1. HTML을 열기 전부터 `offline=True`로 설정합니다.
2. 별도 새 브라우저에서는 온라인 상태를 유지하되 HTTP(S)·WebSocket 요청을 차단합니다. 온라인 여부에 따라 코드가 달라져도 외부 의존성을 감지하도록 두 모드를 구분합니다.

두 모드 모두 다음 검사를 수행하도록 구현했습니다. **이번 환경에서는 이 브라우저 검사들을 실행 완료하지 못했습니다.**

- EPUB·PDF·TXT 각각에 대해 TXT·Markdown·JSON의 실제 다운로드와 내용 확인
- 한글, 본문 마커 순서, 장·페이지 구조, JSON의 전체 본문 일치 확인
- 동봉 `예제.epub` 변환과 3가지 다운로드 확인
- native Worker 메시지 관찰: `GetDocRequest`, `GetDoc`, `GetTextContent`, 한글 CMap의 `FetchBinaryData`
- Blob 모듈 워커와 브라우저 워커 대상 생성 확인, PDF.js fake-worker 대체 실행 거부
- 외부 요청 시도, 예상하지 않은 로컬 보조 파일 요청, 워커 오류, 페이지 예외 검사
- 기존 보호·스캔·손상 입력 거부, 취소, 재선택, 초기화, 좁은 화면 검사 유지

HTTP(S)/WebSocket 차단과 이벤트 관찰은 브라우저 컨텍스트의 요청을 검사합니다. 운영체제 전체나 브라우저 업데이트·확장 프로그램의 트래픽을 패킷 캡처하는 검사는 아닙니다.

## 재현 명령

저장소 루트에서 Node.js 24 이상과 Python 3을 사용합니다. 이미 배포된 ZIP을 확인할 때는 먼저 재빌드하지 않습니다.

```sh
python3 -m pip install 'playwright>=1.48' reportlab pypdf
python3 -m playwright install chromium
python3 tools/book-text/test-release.py
```

이미 설치된 Chrome/Chromium은 `CHROMIUM_EXECUTABLE`로 지정할 수 있습니다. 예를 들어 macOS에서는 다음과 같습니다. 이 명령은 제공만 했으며 실제 Mac에서 실행하지 않았습니다.

```sh
CHROMIUM_EXECUTABLE='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' \
  python3 tools/book-text/test-release.py
```

브라우저 실행이 불가능하면 정적 검사만 수행합니다. 종료 메시지와 JSON에 `static-only`가 표시되고 브라우저 통과로 기록되지 않습니다.

```sh
python3 tools/book-text/test-release.py --static-only
```

기본 결과 위치는 `tools/book-text/test-output/file-release/`입니다. 전체 결과는 `release-report.json`, 각 브라우저의 출력은 `offline/run.log`와 `network-blocked/run.log`에 기록됩니다. 브라우저 검사에 성공하면 각 모드에 `browser-report.json`과 미리보기 이미지도 생성됩니다. 실패 시 명령은 0이 아닌 종료 코드를 반환합니다.

소스를 수정한 경우 다음 순서로 새 배포 ZIP을 검증합니다.

```sh
npm ci --prefix tools/book-text
npm run build --prefix tools/book-text
python3 tools/book-text/build-release.py
python3 tools/book-text/test-release.py
```

기존 HTTP 환경과 비교하려면 `python3 tools/book-text/test-browser.py --transport http`를 사용합니다. 이 결과를 `file://` 검증 결과로 취급하지 않습니다.
