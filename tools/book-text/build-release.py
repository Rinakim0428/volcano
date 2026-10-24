"""Create a public deliverable containing only this tool and authored demo text."""
from datetime import datetime
from io import BytesIO
from pathlib import Path
from zoneinfo import ZoneInfo
import hashlib
import zipfile

root = Path(__file__).resolve().parent
repo = root.parent.parent
timestamp = datetime.now(ZoneInfo('Asia/Seoul')).timetuple()[:6]

sample = BytesIO()
with zipfile.ZipFile(sample, 'w') as z:
    z.writestr('mimetype', 'application/epub+zip', compress_type=zipfile.ZIP_STORED)
    z.writestr('META-INF/container.xml', '''<?xml version="1.0" encoding="UTF-8"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>''')
    z.writestr('book.opf', '''<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="book-id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="book-id">book-text-studio-demo</dc:identifier><dc:title>본문 변환 예제</dc:title><dc:creator>기능 확인용 직접 작성 예제</dc:creator><dc:language>ko</dc:language></metadata><manifest><item id="one" href="one.xhtml" media-type="application/xhtml+xml"/><item id="two" href="two.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="one"/><itemref idref="two"/></spine></package>''')
    for path, title, content in [
        ('one.xhtml', '1장 — 파일에서 본문 읽기', '이 글은 본문 변환 기능을 확인하기 위해 만든 예제입니다. 한글과 문단이 올바르게 표시되는지 확인하세요.'),
        ('two.xhtml', '2장 — 텍스트 파일 저장', '본문 TXT 다운로드를 누르면 두 장의 글이 하나의 텍스트 파일에 저장됩니다. Markdown과 JSON으로도 저장할 수 있습니다.'),
    ]:
        z.writestr(path, f'<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>{title}</title></head><body><h1>{title}</h1><p>{content}</p></body></html>')

entries = [
    ('Book-Text-Studio.html', (repo / 'downloads/Book-Text-Studio.html').read_bytes()),
    ('사용방법.txt', (root / '사용방법.txt').read_bytes()),
    ('예제.epub', sample.getvalue()),
    ('THIRD-PARTY-NOTICES.txt', (root / 'THIRD-PARTY-NOTICES.txt').read_bytes()),
]
output = repo / 'downloads/Book-Text-Studio.zip'
with zipfile.ZipFile(output, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
    for name, data in entries:
        entry = zipfile.ZipInfo('Book-Text-Studio/' + name, date_time=timestamp)
        entry.create_system = 3
        entry.external_attr = 0o100644 << 16
        entry.compress_type = zipfile.ZIP_DEFLATED
        archive.writestr(entry, data)
with zipfile.ZipFile(output) as archive:
    if archive.testzip() is not None:
        raise RuntimeError('ZIP CRC check failed')
digest = hashlib.sha256(output.read_bytes()).hexdigest()
(repo / 'downloads/Book-Text-Studio.zip.sha256').write_text(digest + '  Book-Text-Studio.zip\n')
print(f'Built {output.name}: {output.stat().st_size:,} bytes; SHA256 {digest}')
