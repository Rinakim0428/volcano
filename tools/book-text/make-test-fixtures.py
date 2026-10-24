"""Create only synthetic ebook/PDF fixtures for browser integration tests."""
from pathlib import Path
import io
import json
import sys
import zipfile

from reportlab.pdfgen import canvas
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.cidfonts import UnicodeCIDFont
from pypdf import PdfReader, PdfWriter

destination = Path(sys.argv[1]).resolve()
destination.mkdir(parents=True, exist_ok=True)

CONTAINER = '''<?xml version="1.0" encoding="UTF-8"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="OEBPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>'''


def epub(filename, *, chapters=2, encryption=None, traversal=False):
    manifest = []
    spine = []
    documents = []
    for number in range(1, chapters + 1):
        marker = 'FIRST_MARKER' if number == 1 else 'SECOND_MARKER' if number == 2 else f'CHAPTER_{number}_MARKER'
        href = '../../outside.xhtml' if traversal and number == 1 else f'Text/chapter-{number}.xhtml'
        manifest.append(f'<item id="c{number}" href="{href}" media-type="application/xhtml+xml"/>')
        spine.append(f'<itemref idref="c{number}"/>')
        body = f'''<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>{number}장</title></head><body>
<h1>{number}장</h1><p>{marker}</p><p>한글 본문을 확인합니다.</p><p title="&QUOT;인용&QUOT;">기호 &amp; 문장과&nbsp;공백.</p>
<script>window.INJECTED=true</script><img src="https://example.invalid/SHOULD_NOT_FETCH.png" alt="이미지의 글자"/>
<p>둘째 문단입니다.<br/>줄바꿈을 보존합니다.</p></body></html>'''
        documents.append((f'OEBPS/Text/chapter-{number}.xhtml', body))
    if encryption == 'font':
        manifest.append('<item id="font" href="Fonts/test.otf" media-type="font/otf"/>')
    package = f'''<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="book-id">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="book-id">urn:synthetic:book</dc:identifier><dc:title>테스트 전자책</dc:title><dc:creator>시험 저자</dc:creator><dc:language>ko</dc:language></metadata>
<manifest>{''.join(reversed(manifest))}</manifest><spine>{''.join(spine)}</spine></package>'''
    with zipfile.ZipFile(destination / filename, 'w') as archive:
        archive.writestr('mimetype', 'application/epub+zip', compress_type=zipfile.ZIP_STORED)
        archive.writestr('META-INF/container.xml', CONTAINER, compress_type=zipfile.ZIP_DEFLATED)
        archive.writestr('OEBPS/package.opf', package, compress_type=zipfile.ZIP_DEFLATED)
        for name, body in documents:
            archive.writestr(name, body, compress_type=zipfile.ZIP_DEFLATED)
        if encryption:
            algorithm = 'http://www.idpf.org/2008/embedding' if encryption == 'font' else 'http://www.w3.org/2001/04/xmlenc#aes256-cbc'
            target = 'OEBPS/Fonts/test.otf' if encryption == 'font' else 'OEBPS/Text/chapter-1.xhtml'
            archive.writestr('META-INF/encryption.xml', f'''<encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><EncryptedData xmlns="http://www.w3.org/2001/04/xmlenc#"><EncryptionMethod Algorithm="{algorithm}"/><CipherData><CipherReference URI="{target}"/></CipherData></EncryptedData></encryption>''')
            if encryption == 'font':
                archive.writestr(target, b'SYNTHETIC OBFUSCATED FONT; NEVER READ BY EXTRACTOR')


epub('book.epub')
epub('font-obfuscated.epub', encryption='font')
epub('encrypted.epub', encryption='aes')
epub('traversal.epub', traversal=True)
epub('long.epub', chapters=100)

pdfmetrics.registerFont(UnicodeCIDFont('HYSMyeongJo-Medium'))
pdf = canvas.Canvas(str(destination / 'book.pdf'))
pdf.setTitle('테스트 PDF')
pdf.setAuthor('시험 저자')
for number, marker in [(1, 'FIRST_MARKER'), (2, 'SECOND_MARKER')]:
    pdf.setFont('Helvetica', 12)
    pdf.drawString(72, 760, marker)
    pdf.setFont('HYSMyeongJo-Medium', 12)
    pdf.drawString(72, 730, '한글 본문을 확인합니다.')
    pdf.showPage()
pdf.save()

scanned = canvas.Canvas(str(destination / 'scanned.pdf'))
scanned.setTitle('텍스트 없는 시험 PDF')
scanned.rect(72, 72, 200, 200, fill=1)
scanned.showPage()
scanned.save()

for filename, password in [('encrypted.pdf', 'fixture-password'), ('encrypted-empty.pdf', '')]:
    writer = PdfWriter()
    reader = PdfReader(destination / 'book.pdf')
    for page in reader.pages:
        writer.add_page(page)
    writer.add_metadata({'/Title': '보호된 시험 PDF'})
    writer.encrypt(user_password=password, owner_password='fixture-owner')
    with (destination / filename).open('wb') as stream:
        writer.write(stream)

(destination / 'plain.txt').write_text('한글 본문을 확인합니다.\n\nFIRST_MARKER\nSECOND_MARKER\n', encoding='utf-8')
(destination / 'protected.ncg').write_bytes(b'SYNTHETIC PROTECTED BYTES')
(destination / 'invalid-encoding.txt').write_bytes(b'not utf8: \xff\xfe\xfa')
(destination / 'empty.txt').write_bytes(b'')
(destination / 'unknown.bin').write_bytes(b'unsupported format')
(destination / 'expected.json').write_text(json.dumps({
    'epubTitle': '테스트 전자책', 'pdfTitle': '테스트 PDF', 'author': '시험 저자',
    'koreanText': '한글 본문을 확인합니다.', 'orderedMarkers': ['FIRST_MARKER', 'SECOND_MARKER'],
    'blockedMarker': 'window.INJECTED=true', 'remoteImage': 'https://example.invalid/SHOULD_NOT_FETCH.png',
}, ensure_ascii=False, indent=2), encoding='utf-8')
print('Created synthetic fixtures: ' + ', '.join(sorted(path.name for path in destination.iterdir())))
