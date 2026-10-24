"""Browser checks of the local bridge UI, using a mocked API and file handoff.

Run: python tools/book-text/test-millie-bridge.py
Requires Playwright and Chromium, matching test-browser.py. This does not test
the actual Mac reader, EPUB extraction, or access to any user account.
"""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import json
import os
import shutil
import threading
import time

from playwright.sync_api import sync_playwright, expect


root = Path(__file__).resolve().parent
token = 'a' * 64
state = {'scenario': 'ready', 'requests': []}
handoff_bytes = b'READER_RESPONSE_PASSED_UNCHANGED_TO_CONVERTER'


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def send(self, body, content_type='text/html; charset=utf-8', status=200):
        self.send_response(status)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass  # Expected when the browser aborts an in-flight request.

    def do_GET(self):
        if self.path == '/millie-bridge.js':
            return self.send((root / 'millie-bridge.js').read_bytes(), 'text/javascript')
        harness = '''<script type="module">
          import { initMillieBridge } from '/millie-bridge.js';
          initMillieBridge({ onFile: async file => {
            window.imported = { name: file.name, type: file.type, text: await file.text() };
          }});
        </script>'''
        html = (root / 'ui.html').read_text().replace(
            '__APP_CSS__', (root / 'ui.css').read_text()
        ).replace('<script>__APP_BUNDLE__</script>', harness)
        if 'case=hosted' in self.path:
            html = html.replace('<head>', '<head><meta name="millie-bridge-host" content="1">')
        self.send(html.encode())

    def do_POST(self):
        body = self.rfile.read(int(self.headers['Content-Length']))
        state['requests'].append((self.path, self.headers.get('Authorization'),
                                  json.loads(body), self.headers.get('Cookie')))
        if self.headers.get('Authorization') != 'Bearer ' + token:
            return self.send(b'{"error":{"code":"unauthorized"}}', 'application/json', 401)
        scenario = state['scenario']
        if scenario == 'slow':
            time.sleep(1)
        if self.path == '/api/import':
            if scenario == 'blocked':
                return self.send(b'{"error":{"code":"access_denied","message":"blocked"}}',
                                 'application/json', 403)
            if scenario == 'wrong_type':
                return self.send(b'bad', 'text/html')
            return self.send(handoff_bytes, 'application/epub+zip')
        data = {'state': scenario, 'warnings': [], 'issues': [], 'books': []}
        if scenario in ['ready', 'slow', 'blocked', 'wrong_type']:
            data['books'] = [{
                'title': '<script>alert(1)</script> 책', 'author': '저자', 'chapters': 2,
                'target': {'port': 45231, 'bookSeq': '123456'},
            }]
        self.send(json.dumps(data).encode(), 'application/json')


server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
url = f'http://127.0.0.1:{server.server_port}/'

try:
    with sync_playwright() as p:
        browser = p.chromium.launch(
            executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),
            args=['--no-sandbox'],
        )
        context = browser.new_context(viewport={'width': 1024, 'height': 1100})
        context.add_cookies([{'name': 'unrelated_cookie', 'value': 'must_not_send', 'url': url}])
        page = context.new_page()
        errors = []
        page.on('pageerror', lambda error: errors.append(str(error)))
        page.goto(url)
        expect(page.locator('#millie-panel')).to_be_hidden()
        page.goto(url + '?case=invalid#millie=' + token + 'x')
        expect(page.locator('#millie-panel')).to_be_hidden()
        page.goto(url.replace('127.0.0.1', 'localhost') + '#millie=' + token)
        expect(page.locator('#millie-panel')).to_be_hidden()
        assert not state['requests']

        page.goto(url + '?case=active#millie=' + token)
        expect(page.locator('#millie-panel')).to_be_visible()
        assert '#' not in page.url
        assert not state['requests']  # No automatic discovery or permission request.
        assert page.evaluate('Object.keys(localStorage).length') == 0

        page.locator('#millie-connect').click()
        expect(page.locator('#millie-status')).to_contain_text('열린 책 1권의 정보')
        expect(page.locator('#millie-books strong')).to_have_text('<script>alert(1)</script> 책')
        assert page.locator('#millie-books script').count() == 0
        page.locator('#millie-books button').click()
        expect(page.locator('#millie-status')).to_contain_text('변환을 시작했습니다')
        page.wait_for_function('window.imported')
        assert page.evaluate('window.imported.type') == 'application/epub+zip'
        assert page.evaluate('window.imported.name.endsWith(".epub")')
        assert page.evaluate('window.imported.text') == handoff_bytes.decode()
        assert state['requests'][-1][2] == {'port': 45231, 'bookSeq': '123456'}

        state['scenario'] = 'blocked'
        page.locator('#millie-books button').click()
        expect(page.locator('#millie-status')).to_contain_text('허용하지 않았습니다')
        expect(page.locator('#millie-connect')).to_be_enabled()
        state['scenario'] = 'wrong_type'
        page.locator('#millie-books button').click()
        expect(page.locator('#millie-status')).to_contain_text('형식이 올바르지')

        for scenario, expected in [
            ('app_not_running', '앱을 실행'),
            ('accessibility_required', '손쉬운 사용'),
            ('reader_not_found', '본문을 연 뒤'),
            ('ready_to_probe', '앱의 응답에서 읽기 가능한 본문을 확인하지 못했습니다'),
        ]:
            state['scenario'] = scenario
            page.locator('#millie-connect').click()
            expect(page.locator('#millie-status')).to_contain_text(expected)
            expect(page.locator('#millie-books')).to_be_hidden()

        state['scenario'] = 'slow'
        page.locator('#millie-connect').click()
        expect(page.locator('#millie-cancel')).to_be_visible()
        page.locator('#millie-cancel').click()
        expect(page.locator('#millie-status')).to_contain_text('취소했습니다')
        page.wait_for_timeout(1300)  # The aborted response must not repopulate books.
        expect(page.locator('#millie-books')).to_be_hidden()
        expect(page.locator('#millie-status')).to_contain_text('취소했습니다')
        expect(page.locator('#millie-connect')).to_be_enabled()

        state['scenario'] = 'ready'
        page.locator('#millie-connect').click()
        expect(page.locator('#millie-books')).to_be_visible()
        state['scenario'] = 'slow'
        page.evaluate('delete window.imported')
        page.locator('#millie-books button').click()
        page.locator('#millie-cancel').click()
        page.wait_for_timeout(1300)
        assert page.evaluate('!window.imported')
        expect(page.locator('#millie-status')).to_contain_text('취소했습니다')
        page.set_viewport_size({'width': 390, 'height': 844})
        assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth')
        assert not errors, errors
        assert all(request[1] == 'Bearer ' + token and request[3] is None
                   for request in state['requests'])
        assert page.evaluate('Object.keys(localStorage).length') == 0

        request_count = len(state['requests'])
        page.goto(url + '?case=hosted#millie=' + token)
        expect(page.locator('#millie-panel')).to_be_visible()
        expect(page.locator('#millie-connect')).to_be_enabled()
        assert '#' not in page.url
        page.reload()
        expect(page.locator('#millie-panel')).to_be_visible()
        expect(page.locator('#millie-status')).to_contain_text('연결 세션이 끝났습니다')
        expect(page.locator('#millie-connect')).to_be_disabled()
        assert len(state['requests']) == request_count
        browser.close()
    print('PASS bridge activation, token removal, authentication, omitted cookies, inert titles, '
          'file handoff, state messages, cancellation, stale responses, mobile layout')
finally:
    server.shutdown()
    server.server_close()
