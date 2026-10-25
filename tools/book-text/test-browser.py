"""Body extraction checks. Use test-release.py for the shipped ZIP/file:// suite."""
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from tempfile import TemporaryDirectory
import json
import argparse
import os
import shutil
import subprocess
import sys
import threading
from playwright.sync_api import sync_playwright, expect

root = Path(__file__).resolve().parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('html', nargs='?', type=Path, default=root.parent.parent / 'index.html')
parser.add_argument('--transport', choices=('http', 'file'), default='http')
parser.add_argument('--offline', action='store_true', help='Go offline before navigation (file transport only).')
parser.add_argument('--demo', type=Path, help='Also convert the EPUB shipped inside the ZIP.')
parser.add_argument('--output-dir', type=Path, default=root / 'test-output')
args = parser.parse_args()
if args.offline and args.transport != 'file':
    parser.error('--offline requires --transport file')
html = args.html.resolve()
args.output_dir.mkdir(parents=True, exist_ok=True)

# Observe native Worker messages, without replacing PDF.js or emulating its worker.
# A successful conversion alone could otherwise conceal PDF.js's fake-worker fallback.
WORKER_PROBE = """() => {
  const NativeWorker = window.Worker;
  window.__bookWorkers = [];
  window.Worker = class extends NativeWorker {
    constructor(url, options) {
      super(url, options);
      const record = {url: String(url), type: options?.type, sent: [], received: [], binary: [], errors: []};
      window.__bookWorkers.push(record);
      this.__bookRecord = record;
      this.addEventListener('message', ({data}) => {
        if (data?.action) record.received.push(data.action);
        if (data?.action === 'FetchBinaryData') record.binary.push(data.data);
      });
      this.addEventListener('error', event => record.errors.push(event.message || 'worker error'));
    }
    postMessage(data, ...rest) {
      if (data?.action) this.__bookRecord.sent.push(data.action);
      return super.postMessage(data, ...rest);
    }
  };
}"""

class QuietHandler(SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass

server = None
allowed_origin = None
url = html.as_uri()
if args.transport == 'http':
    server = ThreadingHTTPServer(('127.0.0.1', 0), partial(QuietHandler, directory=str(html.parent)))
    threading.Thread(target=server.serve_forever, daemon=True).start()
    allowed_origin = f'http://127.0.0.1:{server.server_port}/'
    url = allowed_origin + html.name
try:
    with TemporaryDirectory(prefix='book-body-test-') as temporary, sync_playwright() as p:
        fixtures = Path(temporary)
        subprocess.run([sys.executable, str(root / 'make-test-fixtures.py'), str(fixtures)], check=True)
        expected = json.loads((fixtures / 'expected.json').read_text())
        browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
        print(f'ENV Chromium {browser.version}; transport={args.transport}; offline={args.offline}; URL={url}', flush=True)
        context = browser.new_context(accept_downloads=True, offline=args.offline, service_workers='block', viewport={'width': 1280, 'height': 980})
        errors, external, sidecars, console_messages, workers = [], [], [], [], []
        def observe(request):
            address = request.url
            if address.startswith(('https://', 'http://', 'ws://', 'wss://')) and not (allowed_origin and address.startswith(allowed_origin)):
                external.append(address)
            if address.startswith('file:') and address != url:
                sidecars.append(address)
        context.on('request', observe)
        def route_request(route):
            address = route.request.url
            if allowed_origin and address.startswith(allowed_origin):
                route.continue_()
            elif address.startswith(('http:', 'https:')):
                route.abort('internetdisconnected')
            else:
                route.continue_()
        context.route('**/*', route_request)
        def block_socket(route):
            external.append(route.url)
            route.close()
        context.route_web_socket('**/*', block_socket)
        context.add_init_script('(' + WORKER_PROBE + ')()')
        page = context.new_page()
        page.on('pageerror', lambda error: errors.append(str(error)[:300]))
        page.on('console', lambda message: console_messages.append({'type': message.type, 'text': message.text[:500]}))
        page.on('worker', lambda worker: workers.append(worker.url))
        page.goto(url)
        assert page.url == url, page.url
        assert page.evaluate('navigator.onLine') == (not args.offline)
        expect(page.locator('#millie-panel')).to_be_hidden()
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
            assert event.value.suggested_filename.endswith('.' + kind)
            event.value.save_as(target)
            assert event.value.failure() is None
            assert target.stat().st_size > 0
            return target.read_text(encoding='utf-8-sig')

        def check_downloads(text, source_format, chapter_count):
            assert download('txt') == text
            md = download('md').replace('\\_', '_')
            assert expected['koreanText'] in md
            assert md.index('FIRST_MARKER') < md.index('SECOND_MARKER')
            assert md.startswith('# ') and md.count('\n## ') == chapter_count
            data = json.loads(download('json'))
            assert data['text'] == text and data['sourceFormat'] == source_format
            assert len(data['chapters']) == chapter_count
            assert '\n\n'.join(chapter['text'] for chapter in data['chapters']) == text
            return data

        text = ready('book.epub', expected['epubTitle'])
        assert expected['koreanText'] in text
        assert text.index('FIRST_MARKER') < text.index('SECOND_MARKER')
        assert expected['blockedMarker'] not in text
        assert not page.evaluate('Boolean(window.INJECTED)')
        data = check_downloads(text, 'EPUB', 2)
        assert data['author'] == expected['author']
        page.locator('#chapter-select').select_option('1')
        expect(page.locator('#preview')).to_contain_text('SECOND_MARKER')
        assert 'FIRST_MARKER' not in page.locator('#preview').inner_text()
        page.screenshot(path=str(args.output_dir / 'epub-preview.png'), full_page=True)
        print('PASS EPUB actual body, Korean, spine order, inert markup, TXT/Markdown/JSON, chapter preview', flush=True)

        text = ready('font-obfuscated.epub', expected['epubTitle'])
        assert expected['koreanText'] in text
        expect(page.locator('#warnings')).to_contain_text('글꼴')
        print('PASS ordinary font obfuscation does not reject readable EPUB body', flush=True)

        text = ready('book.pdf', expected['pdfTitle'])
        assert expected['koreanText'] in text, repr(text)
        assert text.index('FIRST_MARKER') < text.index('SECOND_MARKER')
        check_downloads(text, 'PDF', 2)
        worker_records = page.evaluate('window.__bookWorkers')
        pdf_workers = [worker for worker in worker_records if 'GetDocRequest' in worker['sent'] and 'GetDoc' in worker['received']]
        assert pdf_workers, f'No real PDF worker completed a document: {worker_records}'
        assert all(worker['url'].startswith('blob:') and worker['type'] == 'module' for worker in pdf_workers)
        assert any('GetTextContent' in worker['sent'] for worker in pdf_workers)
        assert any(item.get('kind') == 'cMapUrl' for worker in pdf_workers for item in worker['binary'])
        assert workers, 'No browser worker target was created'
        print('PASS PDF Korean CID text, embedded CMaps, real Blob module worker, page order, TXT/Markdown/JSON', flush=True)

        text = ready('plain.txt')
        assert expected['koreanText'] in text
        check_downloads(text, 'TXT', 1)
        print('PASS UTF-8 text body and TXT/Markdown/JSON', flush=True)

        if args.demo:
            demo = ready(args.demo.resolve(), '본문 변환 예제')
            assert '이 글은 본문 변환 기능을 확인하기 위해 만든 예제입니다.' in demo
            assert demo.index('1장 —') < demo.index('2장 —')
            assert download('txt') == demo
            assert '두 장의 글이 하나의 텍스트 파일에 저장됩니다.' in download('md')
            assert json.loads(download('json'))['text'] == demo
            print('PASS shipped example EPUB and all three downloads', flush=True)

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
        assert not sidecars, sidecars
        assert not errors, errors
        assert not [message for message in console_messages if 'fake worker' in message['text'].lower()], console_messages
        assert not [error for worker in page.evaluate('window.__bookWorkers') for error in worker['errors']]
        report = {'browser': browser.version, 'transport': args.transport, 'offline': args.offline,
                  'externalRequests': external, 'sidecarRequests': sidecars, 'pageErrors': errors,
                  'pdfWorkers': pdf_workers, 'console': console_messages}
        (args.output_dir / 'browser-report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf-8')
        print('PASS no external requests, sidecar requests, fake-worker fallback, or browser runtime errors', flush=True)
        browser.close()
finally:
    if server:
        server.shutdown()
        server.server_close()
