"""End-to-end body extraction checks with authored fixtures, no Millie account."""
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from tempfile import TemporaryDirectory
import json
import os
import shutil
import subprocess
import sys
import threading
from playwright.sync_api import sync_playwright, expect

root = Path(__file__).resolve().parent
html = Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else root.parent.parent / 'index.html'

class QuietHandler(SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass

server = ThreadingHTTPServer(('127.0.0.1', 0), partial(QuietHandler, directory=str(html.parent)))
threading.Thread(target=server.serve_forever, daemon=True).start()
url = f'http://127.0.0.1:{server.server_port}/{html.name}'
try:
    with TemporaryDirectory(prefix='book-body-test-') as temporary, sync_playwright() as p:
        fixtures = Path(temporary)
        subprocess.run([sys.executable, str(root / 'make-test-fixtures.py'), str(fixtures)], check=True)
        expected = json.loads((fixtures / 'expected.json').read_text())
        browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
        context = browser.new_context(accept_downloads=True, viewport={'width': 1280, 'height': 980})
        errors, external = [], []
        context.on('request', lambda request: external.append(request.url) if request.url.startswith(('https://', 'http://')) and not request.url.startswith(f'http://127.0.0.1:{server.server_port}/') else None)
        page = context.new_page()
        page.on('pageerror', lambda error: errors.append(str(error)[:300]))
        page.goto(url)
        expect(page.locator('#download-txt')).to_be_disabled()

        def load(name):
            page.locator('#book-file').set_input_files(str(fixtures / name))

        def ready(name, title=None):
            load(name)
            expect(page.locator('#status')).to_contain_text('본문 변환을 마쳤습니다', timeout=20000)
            expect(page.locator('#download-txt')).to_be_enabled()
            if title: expect(page.locator('#book-title')).to_have_text(title)
            return page.locator('#preview').inner_text()

        def download(kind):
            with page.expect_download() as event:
                page.locator('#download-' + kind).click()
            target = fixtures / ('export.' + kind)
            event.value.save_as(target)
            return target.read_text(encoding='utf-8-sig')

        text = ready('book.epub', expected['epubTitle'])
        assert expected['koreanText'] in text
        assert text.index('FIRST_MARKER') < text.index('SECOND_MARKER')
        assert expected['blockedMarker'] not in text
        assert not page.evaluate('Boolean(window.INJECTED)')
        assert download('txt') == text
        md = download('md')
        assert 'FIRST_MARKER' in md.replace('\\_', '_') and 'SECOND_MARKER' in md.replace('\\_', '_')
        data = json.loads(download('json'))
        assert len(data['chapters']) == 2 and data['text'] == text
        assert data['author'] == expected['author']
        page.locator('#chapter-select').select_option('1')
        expect(page.locator('#preview')).to_contain_text('SECOND_MARKER')
        assert 'FIRST_MARKER' not in page.locator('#preview').inner_text()
        page.screenshot(path=str(root / 'body-preview.png'), full_page=True)
        print('PASS EPUB actual body, Korean, spine order, inert markup, TXT/Markdown/JSON, chapter preview', flush=True)

        text = ready('font-obfuscated.epub', expected['epubTitle'])
        assert expected['koreanText'] in text
        expect(page.locator('#warnings')).to_contain_text('글꼴')
        print('PASS ordinary font obfuscation does not reject readable EPUB body', flush=True)

        text = ready('book.pdf', expected['pdfTitle'])
        assert expected['koreanText'] in text, repr(text)
        assert text.index('FIRST_MARKER') < text.index('SECOND_MARKER')
        assert download('txt') == text
        assert len(json.loads(download('json'))['chapters']) == 2
        print('PASS PDF actual Korean CID text, embedded CMaps/fonts, page order and downloads', flush=True)

        text = ready('plain.txt')
        assert expected['koreanText'] in text
        assert download('txt') == text
        print('PASS UTF-8 text body and output', flush=True)

        for name, fragment in [
            ('protected.ncg', '보호 파일'), ('encrypted.epub', '보호된'),
            ('encrypted.pdf', '암호'), ('encrypted-empty.pdf', '암호'),
            ('traversal.epub', '범위'), ('scanned.pdf', '텍스트를 찾지 못'),
            ('invalid-encoding.txt', '인코딩'), ('empty.txt', '비어'), ('unknown.bin', '지원'),
        ]:
            load(name)
            expect(page.locator('#status')).to_have_class('status error', timeout=20000)
            expect(page.locator('#status')).to_contain_text(fragment)
            expect(page.locator('#download-txt')).to_be_disabled()
            expect(page.locator('#result')).to_be_hidden()
        print('PASS protected/password/empty-password/scanned/corrupt/unknown inputs reject without stale downloads', flush=True)

        load('long.epub')
        page.locator('#cancel').click(timeout=5000)
        expect(page.locator('#status')).to_contain_text('취소했습니다', timeout=10000)
        expect(page.locator('#download-txt')).to_be_disabled()
        ready('plain.txt')
        load('long.epub')
        ready('plain.txt')
        page.wait_for_timeout(700)
        expect(page.locator('#book-title')).to_have_text('plain')
        page.locator('#reset').click()
        expect(page.locator('#result')).to_be_hidden()
        expect(page.locator('#download-txt')).to_be_disabled()
        ready('plain.txt')
        page.set_viewport_size({'width': 390, 'height': 844})
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
        print('PASS actual cancellation, rapid reselect, reset, and narrow viewport', flush=True)
        assert not external, external
        assert not errors, errors
        print('PASS no remote resource requests or browser runtime errors', flush=True)
        browser.close()
finally:
    server.shutdown()
    server.server_close()
