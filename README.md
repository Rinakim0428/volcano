# Mac 밀리 리더 연결과 책 본문 내보내기

Mac에서 현재 열어 둔 밀리 EPUB 책을 찾아 로컬 리더의 본문 응답을 가져온 뒤 TXT·Markdown·JSON으로 저장하는 **실험용 연결기**를 추가했습니다. 실제 밀리 앱의 본문을 가져오는 데 성공한 상태는 아닙니다. 앱이 현재 책의 URL 속성과 본문을 외부 도구에 제공하는지 실제 Mac에서 확인해야 합니다.

## Mac 연결기

[Mac 연결기 ZIP](https://github.com/Rinakim0428/volcano/raw/refs/heads/codex/book-text-export-20261024/downloads/Millie-Reader-Connect-Mac.zip)을 압축 해제하고 `밀리-연결-시작.command`를 더블클릭합니다. Apple Silicon/Intel용 실행 파일과 화면이 포함돼 있으며, macOS 13 이상에서 사용할 수 있도록 빌드했습니다. 개발 도구 설치나 명령어 입력은 필요하지 않습니다.

공식 밀리 Mac 앱에서 책 본문을 연 상태로, 연결 화면의 **밀리에서 열어 둔 책 연결 → 본문 가져오기 → 본문 TXT 다운로드** 순서로 진행합니다. 제목만 표시된 것은 목차 정보 확인이며, 실제 본문이 변환되어야 저장할 수 있습니다. 앱이 제공한 자료가 미리보기일 수 있어 전체 도서 수집을 보장하지 않습니다.

서명·Apple 공증이 없는 실험 버전이므로 macOS가 실행을 차단하거나 자동화/손쉬운 사용 권한을 요청할 수 있습니다. 실제 Mac 실행과 권한 안내는 검증하지 못했습니다. 자세한 범위와 상태 설명은 [먼저읽기](tools/millie-connect/먼저읽기.txt)에 있습니다.

연결기는 번들 ID를 확인한 `millie` 프로세스의 수신 포트와 현재 리더의 AXURL에서 얻은 `bookSeq`만 사용합니다. 이 주소에서 EPUB container·OPF·spine 본문을 일반 HTTP GET으로 읽습니다. 응답이 거부되거나 보호 형식이면 중단합니다. 인증 정보·쿠키·라이선스를 수집하거나 참조 주소를 위조하지 않습니다. 온라인 서재 전체 다운로드, 로그인·구독 대행, 보호 해제는 구현하지 않았습니다.

## 파일 본문 변환기

사용자가 선택한 EPUB·PDF·TXT의 **실제 본문**을 추출하고 TXT·Markdown·JSON으로 저장하는 브라우저 도구입니다. 필요한 코드, PDF 워커, 한글 등 문자 매핑 자료와 표준 글꼴 자료를 HTML 한 파일에 포함합니다. 입력 파일은 서버에 업로드하지 않습니다.

## 사용

`downloads/Book-Text-Studio.zip`을 받아 압축을 풀고 `Book-Text-Studio.html`을 최신 Chrome에서 엽니다. 책 파일을 선택하면 변환과 미리보기가 시작됩니다. **본문 TXT 다운로드** 버튼으로 전체 본문을 저장합니다. 함께 포함한 `예제.epub`는 직접 작성한 기능 확인용 예제입니다.

## 구현된 범위

- EPUB spine 순서에 따른 장별 본문 추출 및 제목·저자 인식
- 텍스트가 포함된 PDF의 페이지별 본문 추출, 한글 CID 문자 매핑
- UTF-8 TXT 입력과 전체 TXT·장별 Markdown·JSON 출력
- 장별 미리보기, 진행 표시, 취소, 빠른 재선택 시 이전 결과 혼입 방지
- 파일·압축 해제·본문 크기 한도와 손상된 파일 오류 처리

단독 HTML의 파일 선택 기능은 읽을 수 있는 원본 파일을 입력받습니다. Mac 연결기를 통해 받은 본문도 같은 변환기로 처리합니다. **.ncg·암호화 EPUB/PDF의 보호 해제와 OCR은 구현하지 않았습니다.** 특정 상용 도서의 본문과 원본 밀리 앱은 저장소나 배포 파일에 포함하지 않습니다.

파일 한도는 128 MiB, EPUB 항목/PDF 페이지는 2,000개, 본문은 2,000만 글자입니다. PDF의 다단 편집·표·각주와 이미지 속 글자는 원본과 대조해야 합니다. 일반 EPUB의 글꼴 난독화는 본문이 읽을 수 있는 상태이면 허용하고, 암호화된 콘텐츠는 거부합니다.

## 개발과 검증

Node.js 24 이상과 Python 3 환경에서 저장소 루트에서 실행합니다.

```sh
npm ci --prefix tools/book-text --cache /tmp/book-text-npm-cache
npm run build --prefix tools/book-text
python3 tools/book-text/test-browser.py
python3 tools/book-text/test-millie-bridge.py
python3 tools/book-text/test-millie-bridge-bundled.py
python3 tools/book-text/build-release.py
```

브라우저 테스트에는 Python Playwright 1.48 이상, reportlab, pypdf와 Chromium이 필요합니다. `CHROMIUM_EXECUTABLE`로 Chromium 경로를 지정할 수 있습니다. 배포된 HTML을 사용하는 데 이 개발 도구를 설치할 필요는 없습니다.

합성 EPUB/PDF/TXT로 실제 본문과 한글, 읽기 순서, 세 가지 다운로드 파일의 내용, 암호화·스캔·손상 입력 거부, 취소·재선택, 좁은 화면, 외부 요청이 없는 실행을 검증했습니다. **검증은 Linux Chromium의 localhost에서 수행했습니다. 실제 Mac 실행과 실제 밀리 도서 파일은 검증하지 않았습니다.** 관리형 브라우저에서 `file://` 이동이 제한되어 직접 파일 열기 테스트는 수행하지 못했습니다.

### 배포 ZIP의 독립 실행 검증 (2026-10-25)

배포된 ZIP을 먼저 검사하려면 소스를 재빌드하지 않고 다음 명령을 실행합니다.

```sh
python3 -m pip install 'playwright>=1.48' reportlab pypdf
python3 -m playwright install chromium
python3 tools/book-text/test-release.py
```

`test-release.py`는 SHA256·CRC·파일 목록과 내장 스크립트를 검사한 뒤, ZIP을 임시 폴더에 풀고 **그 안의 HTML**을 `file://`로 엽니다. 새 브라우저에서 시작 전 오프라인 설정과 외부 요청 차단 모드를 각각 실행합니다. EPUB/PDF/TXT × TXT/Markdown/JSON의 다운로드 내용, 실제 PDF Blob 워커의 문서 처리, 한글 CMap 요청, 동봉 예제, 외부 요청과 로컬 보조 파일 요청을 확인합니다. HTTP 서버나 `--allow-file-access-from-files`·`--disable-web-security` 옵션은 사용하지 않습니다. 결과와 실패 로그는 `tools/book-text/test-output/file-release/`에 남습니다.

브라우저를 실행할 수 없는 환경에서는 다음 명령으로 **정적 검사만** 실행할 수 있습니다.

```sh
python3 tools/book-text/test-release.py --static-only
```

이번 환경에서는 ZIP 무결성, 배포 HTML 일치, 워커·CMap 168개·표준 글꼴 14개 내장 및 JavaScript 구문 검사를 통과했습니다. 그러나 확보한 Linux Chromium 153.0.8010.0이 시작 직후 `SIGTRAP`으로 종료되어 HTML을 열지 못했습니다. **`file://` 본문 추출·PDF 워커·다운로드·실행 중 외부 요청 없음은 아직 미검증이며, 추가한 브라우저 회귀 테스트도 통과를 확인하지 못했습니다.** 상세 근거와 재현 절차는 [독립 실행 검증 기록](tools/book-text/FILE-OFFLINE-VERIFICATION.md)에 있습니다.

소스를 변경한 후에는 `npm run build --prefix tools/book-text`, `python3 tools/book-text/build-release.py`, `python3 tools/book-text/test-release.py` 순서로 새 배포 ZIP을 검사합니다. Mac 연결기 빌드·병합 및 GitHub Pages 배포는 이 검증에 포함하지 않습니다.

`build.mjs`는 루트 `index.html`과 `downloads/Book-Text-Studio.html`을 생성합니다. `build-release.py`는 HTML, 사용 안내, 직접 작성한 예제와 라이선스를 ZIP으로 묶고 SHA256을 기록합니다. `make-test-fixtures.py`는 테스트용 입력만 생성합니다.

라이선스 전문은 `tools/book-text/THIRD-PARTY-NOTICES.txt`, 배포 ZIP과 HTML에 포함됩니다.

Mac 연결기는 Go 1.27 이상으로 빌드합니다. HTML을 먼저 빌드한 뒤 `python3 tools/millie-connect/build-release.py`를 실행하면 최신 HTML을 embed 경로에 복사하고 두 CPU용 실행 파일과 안내를 ZIP으로 묶습니다. `GO_BINARY`로 Go 실행 파일 경로를 지정할 수 있습니다. 빌드 후 `cd tools/millie-connect && go test -race ./...`로 프로세스 발견·일반 HTTP 본문 수집·세션 및 API 경계 테스트를 실행합니다. 테스트는 합성 프로세스/리더 응답을 사용하며 실제 밀리 연결 성공을 증명하지 않습니다.
