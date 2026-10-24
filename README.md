# 책 본문 내보내기

사용자가 선택한 EPUB·PDF·TXT의 **실제 본문**을 추출하고 TXT·Markdown·JSON으로 저장하는 브라우저 도구입니다. 필요한 코드, PDF 워커, 한글 등 문자 매핑 자료와 표준 글꼴 자료를 HTML 한 파일에 포함합니다. 입력 파일은 서버에 업로드하지 않습니다.

## 사용

`downloads/Book-Text-Studio.zip`을 받아 압축을 풀고 `Book-Text-Studio.html`을 최신 Chrome에서 엽니다. 책 파일을 선택하면 변환과 미리보기가 시작됩니다. **본문 TXT 다운로드** 버튼으로 전체 본문을 저장합니다. 함께 포함한 `예제.epub`는 직접 작성한 기능 확인용 예제입니다.

## 구현된 범위

- EPUB spine 순서에 따른 장별 본문 추출 및 제목·저자 인식
- 텍스트가 포함된 PDF의 페이지별 본문 추출, 한글 CID 문자 매핑
- UTF-8 TXT 입력과 전체 TXT·장별 Markdown·JSON 출력
- 장별 미리보기, 진행 표시, 취소, 빠른 재선택 시 이전 결과 혼입 방지
- 파일·압축 해제·본문 크기 한도와 손상된 파일 오류 처리

**밀리 계정 로그인, 온라인 서재의 도서 자동 다운로드, .ncg·암호화 EPUB/PDF의 보호 해제, OCR은 구현하지 않았습니다.** 이 도구는 책 본문을 가져오는 전체 밀리 연동 시스템이 아니라, 읽을 수 있는 원본 파일을 입력받는 본문 변환 기능입니다. 특정 상용 도서의 본문은 저장소나 배포 파일에 포함하지 않습니다.

파일 한도는 128 MiB, EPUB 항목/PDF 페이지는 2,000개, 본문은 2,000만 글자입니다. PDF의 다단 편집·표·각주와 이미지 속 글자는 원본과 대조해야 합니다. 일반 EPUB의 글꼴 난독화는 본문이 읽을 수 있는 상태이면 허용하고, 암호화된 콘텐츠는 거부합니다.

## 개발과 검증

Node.js 24 이상과 Python 3 환경에서 저장소 루트에서 실행합니다.

```sh
npm ci --prefix tools/book-text --cache /tmp/book-text-npm-cache
npm run build --prefix tools/book-text
python3 tools/book-text/test-browser.py
python3 tools/book-text/build-release.py
```

브라우저 테스트에는 Python Playwright, reportlab, pypdf와 Chromium이 필요합니다. `CHROMIUM_EXECUTABLE`로 Chromium 경로를 지정할 수 있습니다. 이 개발 환경에는 위 도구가 이미 제공되어 있습니다. 배포된 HTML을 사용하는 데 이 개발 도구를 설치할 필요는 없습니다.

합성 EPUB/PDF/TXT로 실제 본문과 한글, 읽기 순서, 세 가지 다운로드 파일의 내용, 암호화·스캔·손상 입력 거부, 취소·재선택, 좁은 화면, 외부 요청이 없는 실행을 검증했습니다. **검증은 Linux Chromium의 localhost에서 수행했습니다. 실제 Mac 실행과 실제 밀리 도서 파일은 검증하지 않았습니다.** 관리형 브라우저에서 `file://` 이동이 제한되어 직접 파일 열기 테스트는 수행하지 못했습니다.

`build.mjs`는 루트 `index.html`과 `downloads/Book-Text-Studio.html`을 생성합니다. `build-release.py`는 HTML, 사용 안내, 직접 작성한 예제와 라이선스를 ZIP으로 묶고 SHA256을 기록합니다. `make-test-fixtures.py`는 테스트용 입력만 생성합니다.

라이선스 전문은 `tools/book-text/THIRD-PARTY-NOTICES.txt`, 배포 ZIP과 HTML에 포함됩니다.
