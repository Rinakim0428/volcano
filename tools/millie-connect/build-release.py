"""Build the Mac-only reader connector; never package source app or user data."""
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo
import hashlib
import json
import os
import shutil
import struct
import subprocess
import tempfile
import zipfile

root = Path(__file__).resolve().parent
repo = root.parent.parent
go = os.environ.get('GO_BINARY') or shutil.which('go')
if not go:
    raise SystemExit('Set GO_BINARY to a Go 1.27+ toolchain.')
html = repo / 'downloads/Book-Text-Studio.html'
assets = root / 'assets'
assets.mkdir(exist_ok=True)
shutil.copyfile(html, assets / html.name)
env = {**os.environ, 'CGO_ENABLED': '0', 'GOOS': 'darwin', 'GOTOOLCHAIN': 'local'}
timestamp = datetime.now(ZoneInfo('Asia/Seoul')).timetuple()[:6]
entries = []
builds = []
with tempfile.TemporaryDirectory(prefix='millie-connect-build-') as temp:
    for arch, cpu in [('arm64', 0x0100000c), ('amd64', 0x01000007)]:
        destination = Path(temp) / ('millie-connect-' + arch)
        subprocess.run([go, 'build', '-trimpath', '-ldflags=-s -w', '-o', str(destination), '.'],
                       cwd=root, env={**env, 'GOARCH': arch}, check=True)
        data = destination.read_bytes()
        magic, actual_cpu = struct.unpack_from('<II', data)
        if magic != 0xfeedfacf or actual_cpu != cpu:
            raise RuntimeError('Unexpected Mach-O architecture')
        load_count = struct.unpack_from('<I', data, 16)[0]
        offset, minimum = 32, None
        for _ in range(load_count):
            command, size = struct.unpack_from('<II', data, offset)
            if command == 0x32:
                version = struct.unpack_from('<I', data, offset + 12)[0]
                minimum = f'{version >> 16}.{(version >> 8) & 255}.{version & 255}'
            offset += size
        builds.append({'architecture': arch, 'bytes': len(data), 'minimumMacOS': minimum,
                       'sha256': hashlib.sha256(data).hexdigest()})
        entries.append(('bin/' + destination.name, data, 0o100755))

go_root = subprocess.check_output([go, 'env', 'GOROOT'], text=True).strip()
entries.extend([
    ('밀리-연결-시작.command', (root / '밀리-연결-시작.command').read_bytes(), 0o100755),
    ('먼저읽기.txt', (root / '먼저읽기.txt').read_bytes(), 0o100644),
    ('THIRD-PARTY-NOTICES.txt', (repo / 'tools/book-text/THIRD-PARTY-NOTICES.txt').read_bytes(), 0o100644),
    ('GO-LICENSE.txt', (Path(go_root) / 'LICENSE').read_bytes(), 0o100644),
    ('BUILD-INFO.json', (json.dumps({'version': '0.1.0-experimental',
        'toolchain': subprocess.check_output([go, 'version'], text=True).strip(),
        'binaries': builds, 'actualMacRuntimeTested': False,
        'actualMillieBodyImported': False}, ensure_ascii=False, indent=2) + '\n').encode(), 0o100644),
])
output = repo / 'downloads/Millie-Reader-Connect-Mac.zip'
with zipfile.ZipFile(output, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
    for name, data, mode in entries:
        entry = zipfile.ZipInfo('Millie-Reader-Connect/' + name, date_time=timestamp)
        entry.create_system = 3
        entry.external_attr = mode << 16
        entry.compress_type = zipfile.ZIP_DEFLATED
        archive.writestr(entry, data)
with zipfile.ZipFile(output) as archive:
    if archive.testzip() is not None:
        raise RuntimeError('ZIP CRC check failed')
digest = hashlib.sha256(output.read_bytes()).hexdigest()
output.with_suffix('.zip.sha256').write_text(digest + '  ' + output.name + '\n')
print(json.dumps({'file': output.name, 'bytes': output.stat().st_size, 'sha256': digest,
                  'binaries': builds}, ensure_ascii=False))
