"""Actual bundled UI -> mocked reader API -> EPUB conversion -> TXT/JSON export.

Build first with `npm run build` in tools/book-text, then run:
python tools/book-text/test-millie-bridge-bundled.py [path/to/generated.html]
Only synthetic book content is used. This does not test the actual Mac reader.
"""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from io import BytesIO
from pathlib import Path
from tempfile import TemporaryDirectory
from zipfile import ZipFile, ZIP_DEFLATED, ZIP_STORED
import json
import os
import shutil
import sys
import threading

from playwright.sync_api import sync_playwright, expect


root = Path(__file__).resolve().parent
html_path = Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else root.parent.parent / 'index.html'
bundle = html_path.read_text()
assert 'millie-bridge-host' in bundle, 'Rebuild the HTML bundle before running this test.'
hosted_bundle = bundle.replace('<head>', '<head><meta name="millie-bridge-host" content="1">').encode()
token = 'c' * 64
target = {'port': 45432, 'bookSeq': 'fixture-book-123'}
title, author = '연결 통합 시험 책', '합성 시험 저자'
requests = []

# Manifest and archive order deliberately differ from spine reading order.
output = BytesIO()
with ZipFile(output, 'w') as archive:
    archive.writestr('mimetype', 'application/epub+zip', compress_type=ZIP_STORED)
    archive.writestr('META-INF/container.xml', '''<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
<rootfiles><rootfile full-path="book.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>''', compress_type=ZIP_DEFLATED)
    archive.writestr('book.opf', f'''<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">synthetic-bridge-book</dc:identifier>
<dc:title>{title}</dc:title><dc:creator>{author}</dc:creator><dc:language>ko</dc:language></metadata>
<manifest><item id="second" href="second.xhtml" media-type="application/xhtml+xml"/>
<item id="first" href="first.xhtml" media-type="application/xhtml+xml"/></manifest>
<spine><itemref idref="first"/><itemref idref="second"/></spine></package>''', compress_type=ZIP_DEFLATED)
    for name, number, marker in [('second', 2, 'SECOND_BODY_MARKER'), ('first', 1, 'FIRST_BODY_MARKER')]:
        archive.writestr(f'{name}.xhtml', f'''<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>{number}장</title></head>
<body><h1>{number}장</h1><p>{marker}</p><p>연결한 책의 한글 본문입니다.</p>
<script>window.BOOK_SCRIPT_EXECUTED=true</script>
<img src="https://example.invalid/do-not-load.png" alt="외부 그림"/>
<p>문장과 &amp; 기호를 보존합니다.</p></body></html>''', compress_type=ZIP_DEFLATED)
epub_bytes = output.getvalue()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def send(self, body, content_type, status=200):
        self.send_response(status)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == '/standalone.html':
            return self.send(bundle.encode(), 'text/html; charset=utf-8')
        self.send(hosted_bundle, 'text/html; charset=utf-8')

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        requests.append((self.path, body, self.headers.get('Authorization'), self.headers.get('Cookie')))
        if self.headers.get('Authorization') != 'Bearer ' + token:
            return self.send(b'{"error":{"code":"unauthorized"}}', 'application/json', 401)
        if self.path == '/api/discover':
            data = {'state': 'readable_books', 'targets': [target], 'warnings': [], 'issues': [],
                    'books': [{'title': title, 'author': author, 'chapters': 2, 'target': target}]}
            return self.send(json.dumps(data).encode(), 'application/json')
        if self.path == '/api/import' and body == target:
            return self.send(epub_bytes, 'application/epub+zip')
        self.send(b'{"error":{"code":"INVALID_TARGET"}}', 'application/json', 400)


server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
origin = f'http://127.0.0.1:{server.server_port}'

try:
    with TemporaryDirectory(prefix='bridge-bundle-test-') as temporary, sync_playwright() as p:
        browser = p.chromium.launch(
            executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),
            args=['--no-sandbox'],
        )
        context = browser.new_context(accept_downloads=True)
        context.add_cookies([{'name': 'unrelated_cookie', 'value': 'do_not_send', 'url': origin}])
        external, errors = [], []
        context.on('request', lambda request: external.append(request.url)
                   if request.url.startswith(('http:', 'https:')) and not request.url.startswith(origin + '/') else None)
        page = context.new_page()
        page.on('pageerror', lambda error: errors.append(str(error)))
        page.goto(origin + '/standalone.html')
        expect(page.locator('#millie-panel')).to_be_hidden()
        page.goto(origin + '/')
        expect(page.locator('#millie-status')).to_contain_text('연결 세션이 끝났습니다')
        expect(page.locator('#millie-connect')).to_be_disabled()
        assert not requests

        page.goto(origin + '/?active=1#millie=' + token)
        expect(page.locator('#millie-connect')).to_be_enabled()
        expect(page.locator('#download-txt')).to_be_disabled()
        assert '#' not in page.url
        assert not requests
        page.locator('#millie-connect').click()
        expect(page.locator('#millie-books strong')).to_have_text(title)
        expect(page.locator('#download-txt')).to_be_disabled()
        page.locator('#millie-books button').click()
        expect(page.locator('#status')).to_contain_text('본문 변환을 마쳤습니다', timeout=20000)
        expect(page.locator('#download-txt')).to_be_enabled()
        expect(page.locator('#book-title')).to_have_text(title)
        expect(page.locator('#book-author')).to_have_text(author)
        expect(page.locator('#chapter-count')).to_have_text('2')
        text = page.locator('#preview').inner_text()
        assert text.index('FIRST_BODY_MARKER') < text.index('SECOND_BODY_MARKER')
        assert '연결한 책의 한글 본문입니다.' in text
        assert '문장과 & 기호를 보존합니다.' in text
        assert 'BOOK_SCRIPT_EXECUTED' not in text
        assert not page.evaluate('Boolean(window.BOOK_SCRIPT_EXECUTED)')

        with page.expect_download() as event:
            page.locator('#download-txt').click()
        txt_path = Path(temporary) / 'body.txt'
        event.value.save_as(txt_path)
        assert txt_path.read_text(encoding='utf-8-sig') == text
        with page.expect_download() as event:
            page.locator('#download-json').click()
        json_path = Path(temporary) / 'body.json'
        event.value.save_as(json_path)
        data = json.loads(json_path.read_text(encoding='utf-8-sig'))
        assert data['title'] == title and data['author'] == author
        assert len(data['chapters']) == 2 and data['text'] == text
        page.locator('#chapter-select').select_option('1')
        expect(page.locator('#preview')).to_contain_text('SECOND_BODY_MARKER')
        assert 'FIRST_BODY_MARKER' not in page.locator('#preview').inner_text()
        assert [request[0] for request in requests] == ['/api/discover', '/api/import']
        assert requests[0][1] == {} and requests[1][1] == target
        assert all(request[2] == 'Bearer ' + token and request[3] is None for request in requests)
        assert not external, external
        assert not errors, errors
        assert page.evaluate('Object.keys(localStorage).length') == 0
        page.reload()
        expect(page.locator('#millie-status')).to_contain_text('연결 세션이 끝났습니다')
        expect(page.locator('#millie-connect')).to_be_disabled()
        assert len(requests) == 2
        browser.close()
    print('PASS bundled bridge discovery -> EPUB body import -> actual conversion -> TXT/JSON download; '
          'Korean, spine order, inert markup, no external network/cookies, expired-session reload')
finally:
    server.shutdown()
    server.server_close()
