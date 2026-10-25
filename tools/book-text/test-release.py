"""Verify the shipped ZIP, then run its extracted HTML via file:// in fresh browsers.

No HTTP server, rebuild, sidecar assets, permissive file-access flags, or warm cache.
Requires the same Python Playwright/reportlab/pypdf dependencies as test-browser.py.
"""
import argparse
import base64
import hashlib
from html.parser import HTMLParser
import json
from pathlib import Path
import subprocess
import sys
from tempfile import TemporaryDirectory
from zipfile import ZipFile

ROOT = Path(__file__).resolve().parent
REPO = ROOT.parent.parent
MEMBERS = {'Book-Text-Studio/' + name for name in (
    'Book-Text-Studio.html', '사용방법.txt', '예제.epub', 'THIRD-PARTY-NOTICES.txt',
)}


class InlineHTML(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=False)
        self.scripts = []
        self.in_script = False
        self.resource_attributes = []

    def handle_starttag(self, tag, attributes):
        attributes = dict(attributes)
        if tag == 'script':
            self.in_script = True
            self.scripts.append('')
        # These elements could load a sidecar or remote resource when the HTML opens.
        for attribute in ('src', 'srcset', 'poster', 'data'):
            if attribute in attributes:
                self.resource_attributes.append((tag, attribute, attributes[attribute]))
        if tag in ('link', 'base') and 'href' in attributes:
            self.resource_attributes.append((tag, 'href', attributes['href']))

    def handle_endtag(self, tag):
        if tag == 'script':
            self.in_script = False

    def handle_data(self, data):
        if self.in_script:
            self.scripts[-1] += data


def inspect_html(html):
    parsed = InlineHTML()
    parsed.feed(html.read_text(encoding='utf-8'))
    assert not parsed.resource_attributes, parsed.resource_attributes
    assert len(parsed.scripts) == 1, 'Expected one inline application script'
    script = parsed.scripts[0]
    def embedded(name):
        prefix = f'globalThis.{name}='
        assert script.count(prefix) == 1, f'Missing/ambiguous embedded {name}'
        return json.JSONDecoder().raw_decode(script.split(prefix, 1)[1])[0]
    worker = embedded('BOOK_PDF_WORKER')
    data = embedded('BOOK_PDF_DATA')
    assert isinstance(worker, str) and 'WorkerMessageHandler' in worker
    assert data['cMapUrl']['UniKS-UCS2-H.bcmap']
    assert data['cMapUrl']['Adobe-Korea1-UCS2.bcmap']
    assert data['standardFontDataUrl']
    for collection in data.values():
        for name, encoded in collection.items():
            assert base64.b64decode(encoded, validate=True), f'Empty embedded PDF asset: {name}'
    for label, code, module in [('application', script, False), ('PDF worker', worker, True)]:
        command = ['node', '--check'] + (['--input-type=module'] if module else [])
        result = subprocess.run(command, input=code, text=True, encoding='utf-8', capture_output=True)
        assert result.returncode == 0, f'{label} syntax check failed: {result.stderr}'
    return {'htmlSha256': hashlib.sha256(html.read_bytes()).hexdigest(),
            'embeddedCMaps': len(data['cMapUrl']), 'embeddedFonts': len(data['standardFontDataUrl']),
            'workerBytes': len(worker.encode('utf-8')), 'externalHTMLResources': parsed.resource_attributes}


def unpack_release(archive, destination):
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    checksum = archive.with_suffix(archive.suffix + '.sha256').read_text(encoding='utf-8').split()
    assert checksum == [digest, archive.name], 'Release ZIP SHA256 does not match its manifest'
    with ZipFile(archive) as bundle:
        assert len(bundle.namelist()) == len(MEMBERS) and set(bundle.namelist()) == MEMBERS, bundle.namelist()
        assert bundle.testzip() is None, 'Release ZIP CRC mismatch'
        # Only the exact allowlisted members can be written; no path traversal/symlinks.
        for name in sorted(MEMBERS):
            target = destination / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(bundle.read(name))
    return digest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('zip', nargs='?', type=Path, default=REPO / 'downloads/Book-Text-Studio.zip')
    parser.add_argument('--output-dir', type=Path, default=ROOT / 'test-output' / 'file-release')
    parser.add_argument('--static-only', action='store_true', help='Check packaging/embedded code only; does not verify browser behavior.')
    args = parser.parse_args()
    args.output_dir = args.output_dir.resolve()
    args.output_dir.mkdir(parents=True, exist_ok=True)
    report = {'archive': str(args.zip.resolve()), 'runs': [], 'status': 'failed'}
    try:
        # Korean, whitespace, and URL-significant characters exercise Path.as_uri().
        with TemporaryDirectory(prefix='book-release-한글 # ') as temporary:
            report['sha256'] = unpack_release(args.zip.resolve(), Path(temporary))
            release = Path(temporary) / 'Book-Text-Studio'
            print('PASS ZIP SHA256, CRC, exact file manifest, isolated extraction', flush=True)
            report['static'] = inspect_html(release / 'Book-Text-Studio.html')
            if args.zip.resolve() == (REPO / 'downloads/Book-Text-Studio.zip').resolve():
                assert (release / 'Book-Text-Studio.html').read_bytes() == (REPO / 'downloads/Book-Text-Studio.html').read_bytes(), 'ZIP contains stale HTML'
                assert (release / 'Book-Text-Studio.html').read_bytes() == (REPO / 'index.html').read_bytes(), 'Root HTML differs from release'
                for name in ('사용방법.txt', 'THIRD-PARTY-NOTICES.txt'):
                    assert (release / name).read_bytes() == (ROOT / name).read_bytes(), f'Stale ZIP member: {name}'
            print('PASS inline script/worker syntax, embedded CMaps/fonts, no HTML resource URLs', flush=True)
            modes = () if args.static_only else ('offline', 'network-blocked')
            for mode in modes:
                output = args.output_dir / mode
                command = [sys.executable, str(ROOT / 'test-browser.py'),
                           str(release / 'Book-Text-Studio.html'), '--transport', 'file',
                           '--demo', str(release / '예제.epub'), '--output-dir', str(output)]
                if mode == 'offline':
                    command.append('--offline')
                run = subprocess.run(command, text=True, encoding='utf-8', stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
                output.mkdir(parents=True, exist_ok=True)
                (output / 'run.log').write_text(run.stdout, encoding='utf-8')
                report['runs'].append({'mode': mode, 'exitCode': run.returncode})
                print(run.stdout, end='', flush=True)
                if run.returncode:
                    raise RuntimeError(f'{mode} browser checks failed; see {output / "run.log"}')
            report['status'] = 'static-only-passed' if args.static_only else 'passed'
    finally:
        (args.output_dir / 'release-report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf-8')
    print('PASS static checks only; browser behavior NOT verified' if args.static_only else
          'PASS shipped ZIP: offline and network-blocked file:// checks', flush=True)


if __name__ == '__main__':
    main()
