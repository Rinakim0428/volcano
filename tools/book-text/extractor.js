import * as pdfjsLib from 'pdfjs-dist/legacy/build/pdf.mjs';
import { ZipReader, BlobReader, configure as configureZip } from '@zip.js/zip.js/index-native.js';

export { pdfjsLib };

const LIMITS = Object.freeze({ fileBytes: 128 * 1024 * 1024, expandedBytes: 128 * 1024 * 1024, entryBytes: 24 * 1024 * 1024, metadataBytes: 2 * 1024 * 1024, characters: 20_000_000, chapters: 2000, entries: 20_000 });
const BLOCKS = new Set(['address', 'article', 'aside', 'blockquote', 'caption', 'dd', 'details', 'div', 'dl', 'dt', 'figcaption', 'figure', 'footer', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'header', 'hr', 'li', 'main', 'ol', 'p', 'section', 'summary', 'table', 'tr', 'ul']);
const IGNORE = new Set(['head', 'script', 'style', 'template', 'noscript', 'svg']);
const FONT_OBFUSCATION = new Set(['http://www.idpf.org/2008/embedding', 'http://ns.adobe.com/pdf/enc#RC']);

export class BookExtractionError extends Error {
  constructor(code, message) { super(message); this.name = 'BookExtractionError'; this.code = code; }
}
function fail(code, message) { throw new BookExtractionError(code, message); }
function abort(signal) { if (signal?.aborted) throw new DOMException('변환을 취소했습니다.', 'AbortError'); }
function progress(callback, message, current = 0, total = 0) {
  if (typeof callback === 'function') callback({ message, current, total, percent: total ? 100 * current / total : 0 });
}
function normalize(text) { return text.replace(/\r\n?/g, '\n').replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f]/g, '').replace(/[ \t]+\n/g, '\n').replace(/\n[ \t]+(?=\n)/g, '\n').replace(/\n{3,}/g, '\n\n').trim(); }
function fallbackTitle(name) { return String(name || '제목 없는 책').split(/[\\/]/).pop().replace(/\.(epub|pdf|txt)$/i, '') || '제목 없는 책'; }
function decode(bytes, allowUtf16 = false) {
  let encoding = 'utf-8';
  if (allowUtf16 && bytes[0] === 0xff && bytes[1] === 0xfe) encoding = 'utf-16le';
  if (allowUtf16 && bytes[0] === 0xfe && bytes[1] === 0xff) encoding = 'utf-16be';
  try { return new TextDecoder(encoding, { fatal: true }).decode(bytes); }
  catch (_) { fail('TEXT_ENCODING', '문자 인코딩을 읽을 수 없습니다. TXT는 UTF-8, EPUB 본문은 UTF-8 또는 BOM이 있는 UTF-16 형식이 필요합니다.'); }
}
function xml(source, label) {
  // XML parsing is inert. Never attach ebook markup to the live document.
  if (/<!ENTITY\b/i.test(source)) fail('UNSUPPORTED_XML', `${label}: 외부·사용자 정의 XML 엔티티는 지원하지 않습니다.`);
  const safe = source.replace(/<!DOCTYPE[^>]*>/gi, '').replace(/&[A-Za-z][A-Za-z0-9]{1,31};/g, entity => {
    if (['&amp;', '&lt;', '&gt;', '&quot;', '&apos;'].includes(entity)) return entity;
    // Decode only a single entity token, never user-provided markup.
    const textarea = document.createElement('textarea'); textarea.innerHTML = entity;
    return textarea.value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&apos;');
  });
  const doc = new DOMParser().parseFromString(safe, 'application/xml');
  if (doc.getElementsByTagName('parsererror').length) fail('INVALID_XML', `${label}: EPUB 문서 구조를 읽을 수 없습니다. 올바른 XHTML/XML 파일이 필요합니다.`);
  return doc;
}
function elements(doc, name) { return Array.from(doc.getElementsByTagNameNS('*', name)); }
function cleanMetadata(value) { return String(value || '').replace(/\s+/g, ' ').trim().slice(0, 2000); }
function firstText(doc, name) { return cleanMetadata(elements(doc, name)[0]?.textContent); }
function archiveName(name) {
  if (!name || name.startsWith('/') || /[\\\u0000-\u001f\u007f]/.test(name) || /^[A-Za-z][A-Za-z0-9+.-]*:/.test(name)) fail('UNSAFE_ARCHIVE_PATH', 'EPUB 안에 지원하지 않는 파일 경로가 있습니다.');
  const parts = name.split('/');
  if (parts.some(part => part === '.' || part === '..')) fail('UNSAFE_ARCHIVE_PATH', 'EPUB 안에 상위 폴더를 가리키는 파일 경로가 있습니다.');
  return parts.filter(Boolean).join('/');
}
function resolveHref(baseFile, href) {
  let path;
  try { path = decodeURIComponent(String(href || '').split('#')[0]); }
  catch (_) { fail('INVALID_EPUB_PATH', 'EPUB의 파일 주소를 해석할 수 없습니다.'); }
  if (!path || /[\\\u0000-\u001f\u007f?]/.test(path) || path.startsWith('/') || /^[A-Za-z][A-Za-z0-9+.-]*:/.test(path)) fail('EXTERNAL_EPUB_RESOURCE', 'EPUB의 외부 주소 또는 지원하지 않는 본문 경로는 읽지 않습니다.');
  const parts = baseFile ? baseFile.split('/').slice(0, -1) : [];
  for (const part of path.split('/')) {
    if (!part || part === '.') continue;
    if (part === '..') { if (!parts.length) fail('UNSAFE_ARCHIVE_PATH', 'EPUB 본문 경로가 파일 묶음의 범위를 벗어납니다.'); parts.pop(); }
    else parts.push(part);
  }
  if (!parts.length) fail('INVALID_EPUB_PATH', 'EPUB 본문 파일 주소가 비어 있습니다.');
  return parts.join('/');
}
function bodyText(doc) {
  const body = elements(doc, 'body')[0];
  if (!body) fail('MISSING_EPUB_BODY', 'EPUB 본문 문서에 body 요소가 없습니다.');
  let images = 0;
  function walk(node) {
    if (node.nodeType === 3 || node.nodeType === 4) return node.nodeValue.replace(/\s+/g, ' ');
    if (node.nodeType !== 1) return '';
    const name = node.localName.toLowerCase();
    if (IGNORE.has(name) || node.getAttribute('aria-hidden') === 'true' || node.hasAttribute('hidden')) { if (name === 'svg') images++; return ''; }
    if (name === 'img' || name === 'image') { images++; return ''; }
    if (name === 'br') return '\n';
    if (name === 'pre') return '\n\n' + node.textContent.replace(/\r\n?/g, '\n') + '\n\n';
    const children = Array.from(node.childNodes).map(walk).join('');
    if (name === 'td' || name === 'th') return children + '\t';
    return BLOCKS.has(name) ? '\n' + children + '\n' : children;
  }
  const text = normalize(walk(body));
  const heading = [...elements(body, 'h1'), ...elements(body, 'h2')].find(node => cleanMetadata(node.textContent));
  return { text, heading: cleanMetadata(heading?.textContent), images };
}
function bookResult({ title, author = '', format, chapters, warnings = [] }) {
  const text = chapters.map(chapter => chapter.text).filter(Boolean).join('\n\n');
  if (!text.trim()) fail('NO_TEXT', '읽을 수 있는 본문 텍스트가 없습니다. 이미지로 된 책이나 스캔 PDF의 OCR은 현재 지원하지 않습니다.');
  if (text.length > LIMITS.characters) fail('TEXT_LIMIT', '변환된 본문이 2,000만 글자를 넘습니다. 더 작은 파일로 나누어 주세요.');
  return { title, author, format, chapters, text, warnings: [...new Set(warnings)] };
}

async function extractEpub(file, options) {
  const { signal, onProgress } = options;
  configureZip({ useWebWorkers: false, useCompressionStream: true });
  const reader = new ZipReader(new BlobReader(file), { useWebWorkers: false });
  const files = new Map(); let expandedBytes = 0;
  async function read(name, maximum = LIMITS.entryBytes) {
    abort(signal);
    const entry = files.get(name);
    if (!entry) fail('MISSING_EPUB_ENTRY', `EPUB에 필요한 본문 파일이 없습니다: ${name}`);
    if (entry.encrypted) fail('ENCRYPTED_EPUB', '암호화된 EPUB은 지원하지 않습니다. 외부에서 읽을 수 있는 원본 파일이 필요합니다.');
    if (entry.uncompressedSize > maximum || entry.uncompressedSize + expandedBytes > LIMITS.expandedBytes) fail('EPUB_SIZE_LIMIT', 'EPUB의 압축 해제 크기가 처리 한도를 넘습니다.');
    const chunks = []; let length = 0;
    await entry.getData(new WritableStream({ write(chunk) {
      abort(signal);
      if (length + chunk.byteLength > maximum || expandedBytes + chunk.byteLength > LIMITS.expandedBytes) fail('EPUB_SIZE_LIMIT', 'EPUB의 압축 해제 크기가 처리 한도를 넘습니다.');
      chunks.push(chunk.slice()); length += chunk.byteLength; expandedBytes += chunk.byteLength;
    } }), { signal, checkSignature: true, useWebWorkers: false });
    abort(signal);
    const result = new Uint8Array(length); let offset = 0;
    for (const chunk of chunks) { result.set(chunk, offset); offset += chunk.byteLength; }
    return result;
  }
  try {
    progress(onProgress, 'EPUB 파일 목록을 확인하고 있습니다.');
    let count = 0;
    for await (const entry of reader.getEntriesGenerator({ signal })) {
      abort(signal);
      if (++count > LIMITS.entries) fail('EPUB_ENTRY_LIMIT', 'EPUB 내부 파일이 20,000개를 넘습니다.');
      const name = archiveName(entry.filename);
      if (entry.directory) continue;
      if (files.has(name)) fail('DUPLICATE_EPUB_ENTRY', 'EPUB에 중복된 파일 경로가 있어 안전하게 읽을 수 없습니다.');
      if (entry.encrypted) fail('ENCRYPTED_EPUB', '암호화된 EPUB은 지원하지 않습니다. 외부에서 읽을 수 있는 원본 파일이 필요합니다.');
      files.set(name, entry);
    }
    if (files.has('mimetype')) {
      const type = decode(await read('mimetype', 100)).trim();
      if (type !== 'application/epub+zip') fail('NOT_EPUB', '파일 내부 형식이 EPUB이 아닙니다.');
    }
    const container = xml(decode(await read('META-INF/container.xml', LIMITS.metadataBytes), true), 'EPUB 컨테이너');
    const rootFile = elements(container, 'rootfile').find(node => node.getAttribute('media-type') === 'application/oebps-package+xml') || elements(container, 'rootfile')[0];
    const opfPath = resolveHref('', rootFile?.getAttribute('full-path'));
    const opf = xml(decode(await read(opfPath, LIMITS.metadataBytes), true), 'EPUB 목차 정보');
    const warnings = [];
    if (files.has('META-INF/encryption.xml')) {
      const encryption = xml(decode(await read('META-INF/encryption.xml', LIMITS.metadataBytes), true), 'EPUB 보호 정보');
      for (const encrypted of elements(encryption, 'EncryptedData')) {
        const method = elements(encrypted, 'EncryptionMethod')[0]?.getAttribute('Algorithm');
        if (!FONT_OBFUSCATION.has(method)) fail('ENCRYPTED_EPUB', '보호된 EPUB 본문 또는 암호화 자료가 포함되어 있습니다. 이 도구는 보호를 해제하지 않습니다.');
      }
      if (elements(encryption, 'EncryptedData').length) warnings.push('EPUB의 글꼴 난독화 정보는 텍스트 추출에 사용하지 않았습니다.');
    }
    const manifest = new Map();
    for (const item of elements(opf, 'item')) {
      const id = item.getAttribute('id');
      if (!id || manifest.has(id)) fail('INVALID_EPUB_MANIFEST', 'EPUB의 본문 목록에 없거나 중복된 ID가 있습니다.');
      manifest.set(id, { href: item.getAttribute('href'), type: item.getAttribute('media-type') || '' });
    }
    const spine = elements(opf, 'itemref');
    if (!spine.length) fail('EMPTY_EPUB_SPINE', 'EPUB에 읽기 순서가 없어 본문을 구성할 수 없습니다.');
    if (spine.length > LIMITS.chapters) fail('CHAPTER_LIMIT', 'EPUB의 본문 항목이 2,000개를 넘습니다.');
    const chapters = []; let characters = 0; let imageCount = 0; let emptyCount = 0;
    for (let index = 0; index < spine.length; index++) {
      abort(signal);
      progress(onProgress, `EPUB 본문 ${index + 1}/${spine.length}을 읽고 있습니다.`, index, spine.length);
      const item = manifest.get(spine[index].getAttribute('idref'));
      if (!item) fail('MISSING_EPUB_MANIFEST_ITEM', 'EPUB 읽기 순서가 존재하지 않는 본문 항목을 가리킵니다.');
      if (!['application/xhtml+xml', 'text/html', 'application/xml', 'text/xml'].includes(item.type)) fail('UNSUPPORTED_EPUB_CHAPTER', 'EPUB에 XHTML/XML로 읽을 수 없는 본문 항목이 있습니다. 이미지 본문 OCR은 지원하지 않습니다.');
      const path = resolveHref(opfPath, item.href);
      const doc = xml(decode(await read(path), true), `본문 ${index + 1}`);
      const extracted = bodyText(doc);
      imageCount += extracted.images;
      if (!extracted.text) emptyCount++;
      characters += extracted.text.length + 2;
      if (characters > LIMITS.characters) fail('TEXT_LIMIT', '변환된 본문이 2,000만 글자를 넘습니다.');
      chapters.push({ title: extracted.heading || `${index + 1}장`, text: extracted.text, index: index + 1 });
      // Yield between chapters so cancellation and screen updates are processed.
      await new Promise(resolve => setTimeout(resolve, 0));
    }
    if (imageCount) warnings.push(`이미지·SVG ${imageCount}개는 텍스트로 변환하지 않았습니다. 이미지 안의 글자는 포함되지 않습니다.`);
    if (emptyCount) warnings.push(`텍스트가 없는 본문 항목 ${emptyCount}개가 있습니다. 표지·이미지 페이지인지 원본과 대조하세요.`);
    abort(signal); progress(onProgress, 'EPUB 본문 변환을 마쳤습니다.', spine.length, spine.length);
    return bookResult({ title: firstText(opf, 'title') || fallbackTitle(file.name), author: elements(opf, 'creator').map(node => cleanMetadata(node.textContent)).filter(Boolean).join(', '), format: 'EPUB', chapters, warnings });
  } catch (error) {
    if (signal?.aborted) throw new DOMException('변환을 취소했습니다.', 'AbortError');
    if (error instanceof BookExtractionError || error.name === 'AbortError') throw error;
    fail('INVALID_EPUB', `EPUB을 읽지 못했습니다. 파일 손상 또는 지원하지 않는 압축 형식일 수 있습니다. (${String(error.message || error).slice(0, 180)})`);
  } finally { await reader.close().catch(() => {}); }
}

async function extractPdf(file, options) {
  const { signal, onProgress, pdfOptions = {} } = options;
  abort(signal);
  progress(onProgress, 'PDF 파일을 확인하고 있습니다.');
  const bytes = new Uint8Array(await file.arrayBuffer()); abort(signal);
  let task; let documentProxy; let destroyPromise;
  const destroy = () => {
    if (!destroyPromise && task) destroyPromise = task.destroy().catch(() => {});
    return destroyPromise;
  };
  try {
    task = pdfjsLib.getDocument({ ...pdfOptions, data: bytes, useWorkerFetch: false, isEvalSupported: false, disableFontFace: true, useSystemFonts: true });
    signal?.addEventListener('abort', destroy, { once: true });
    documentProxy = await task.promise; abort(signal);
    if (documentProxy.numPages > LIMITS.chapters) fail('PDF_PAGE_LIMIT', 'PDF가 2,000쪽을 넘습니다. 더 작은 파일로 나누어 주세요.');
    let metadata;
    try { metadata = await documentProxy.getMetadata(); } catch (_) {
      abort(signal);
      fail('PDF_METADATA_UNREADABLE', 'PDF 구조와 보호 상태를 확인하지 못해 변환을 중단했습니다.');
    }
    abort(signal);
    if (metadata?.info?.EncryptFilterName) fail('ENCRYPTED_PDF', '암호화 또는 권한 제한이 있는 PDF는 지원하지 않습니다. 보호되지 않은 PDF 파일이 필요합니다.');
    const chapters = []; const warnings = []; let characters = 0; let emptyPages = 0;
    for (let pageNumber = 1; pageNumber <= documentProxy.numPages; pageNumber++) {
      abort(signal); progress(onProgress, `PDF ${pageNumber}/${documentProxy.numPages}쪽을 읽고 있습니다.`, pageNumber - 1, documentProxy.numPages);
      const page = await documentProxy.getPage(pageNumber);
      let content;
      try { content = await page.getTextContent({ includeMarkedContent: false }); } finally { page.cleanup(); }
      abort(signal);
      const lines = []; let line = ''; let previous;
      for (const item of content.items) {
        if (typeof item.str !== 'string') continue;
        const changedLine = previous && !previous.hasEOL && Math.abs((item.transform?.[5] || 0) - (previous.transform?.[5] || 0)) > Math.max(2, Math.abs(item.height || previous.height || 10) * 0.5);
        if (changedLine && line) { lines.push(line); line = ''; }
        if (line && previous && !/\s$/.test(line) && !/^\s/.test(item.str)) {
          const gap = (item.transform?.[4] || 0) - ((previous.transform?.[4] || 0) + (previous.width || 0));
          if (gap > Math.max(1, Math.abs(item.height || previous.height || 10) * 0.15)) line += ' ';
        }
        line += item.str;
        if (item.hasEOL) { lines.push(line); line = ''; }
        previous = item;
      }
      if (line) lines.push(line);
      const text = normalize(lines.join('\n'));
      if (!text) emptyPages++;
      characters += text.length + 2;
      if (characters > LIMITS.characters) fail('TEXT_LIMIT', '변환된 본문이 2,000만 글자를 넘습니다.');
      chapters.push({ title: `${pageNumber}쪽`, text, index: pageNumber });
    }
    if (emptyPages === documentProxy.numPages) fail('SCANNED_PDF', 'PDF에서 텍스트를 찾지 못했습니다. 스캔·이미지 PDF의 OCR은 현재 지원하지 않습니다.');
    if (emptyPages) warnings.push(`텍스트가 없는 페이지 ${emptyPages}쪽은 이미지·빈 페이지일 수 있습니다. 해당 페이지의 글자는 포함되지 않습니다.`);
    warnings.push('PDF의 다단 편집·표·복잡한 배치에서는 읽기 순서가 달라질 수 있습니다. 원본과 대조하세요.');
    abort(signal); progress(onProgress, 'PDF 본문 변환을 마쳤습니다.', documentProxy.numPages, documentProxy.numPages);
    return bookResult({ title: cleanMetadata(metadata?.info?.Title) || fallbackTitle(file.name), author: cleanMetadata(metadata?.info?.Author), format: 'PDF', chapters, warnings });
  } catch (error) {
    if (signal?.aborted) throw new DOMException('변환을 취소했습니다.', 'AbortError');
    if (error instanceof BookExtractionError || error.name === 'AbortError') throw error;
    if (error.name === 'PasswordException') fail('ENCRYPTED_PDF', '암호가 필요한 PDF는 지원하지 않습니다. 암호 없이 읽을 수 있는 파일이 필요합니다.');
    fail('INVALID_PDF', `PDF를 읽지 못했습니다. 파일 손상 또는 지원하지 않는 형식일 수 있습니다. (${String(error.message || error).slice(0, 180)})`);
  } finally {
    signal?.removeEventListener('abort', destroy);
    await destroy();
  }
}

export async function extractBook(file, options = {}) {
  const { signal, onProgress } = options;
  abort(signal);
  if (!file || typeof file.arrayBuffer !== 'function') fail('NO_FILE', '먼저 EPUB·PDF·TXT 파일을 선택하세요.');
  if (!file.size) fail('EMPTY_FILE', '비어 있는 파일은 변환할 수 없습니다.');
  if (file.size > LIMITS.fileBytes) fail('FILE_SIZE_LIMIT', '파일은 128 MiB 이하만 지원합니다.');
  const extension = String(file.name || '').split('.').pop().toLowerCase();
  if (extension === 'ncg') fail('PROTECTED_FILE', '밀리 앱 전용 보호 파일(.ncg)은 이 도구에서 본문을 읽거나 텍스트로 변환할 수 없습니다.');
  if (extension === 'epub') return extractEpub(file, options);
  if (extension === 'pdf') return extractPdf(file, options);
  if (extension !== 'txt') fail('UNSUPPORTED_FORMAT', 'EPUB·PDF·UTF-8 TXT 파일만 지원합니다. 앱 설치 파일이나 독서 기록 DB는 책 본문 파일이 아닙니다.');
  progress(onProgress, '텍스트 파일을 읽고 있습니다.');
  const bytes = new Uint8Array(await file.arrayBuffer()); abort(signal);
  const text = normalize(decode(bytes));
  const title = fallbackTitle(file.name);
  const result = bookResult({ title, format: 'TXT', chapters: [{ title, text, index: 1 }] });
  progress(onProgress, '텍스트 파일을 읽었습니다.', 1, 1);
  return result;
}
