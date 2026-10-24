import { extractBook } from './extractor.js';
import { pdfOptions } from './pdf-bootstrap.js';

const el = id => document.getElementById(id);
const PREVIEW_LIMIT = 50000;
const downloadButtons = ['download-txt', 'download-md', 'download-json'];
let generation = 0;
let active = null;
let result = null;
let source = null;
let phase = 'idle';

function formatBytes(value) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KiB`;
  return `${(value / 1024 ** 2).toFixed(1)} MiB`;
}

function setStatus(message, kind = '') {
  el('status').textContent = message;
  el('status').className = `status ${kind}`.trim();
}

function clearResult() {
  result = null;
  el('result').hidden = true;
  el('book-title').textContent = '';
  el('book-author').textContent = '';
  el('preview').textContent = '';
  el('warning-list').replaceChildren();
  el('warnings').hidden = true;
  el('chapter-select').replaceChildren(new Option('전체 본문', 'all'));
  for (const id of downloadButtons) el(id).disabled = true;
}

function updateControls() {
  const loading = phase === 'loading';
  el('cancel').hidden = !loading;
  el('cancel').disabled = false;
  el('reset').hidden = !source && phase === 'idle';
  el('progress').hidden = !loading;
  el('source-heading').textContent = loading ? '책 본문을 읽고 있습니다' : '책 파일 한 권을 추가하세요';
  el('choose-book').textContent = source ? '다른 책 선택' : '책 파일 선택';
  el('drop-zone').setAttribute('aria-busy', String(loading));
  for (const id of downloadButtons) el(id).disabled = phase !== 'ready' || !result;
}

function reset() {
  generation += 1;
  active?.abort();
  active = null;
  source = null;
  phase = 'idle';
  clearResult();
  el('file-name').textContent = '';
  el('file-name').hidden = true;
  el('book-file').value = '';
  el('progress').value = 0;
  setStatus('파일을 선택하면 본문 변환을 시작합니다.');
  updateControls();
}

function failSelection(message) {
  reset();
  phase = 'error';
  setStatus(message, 'error');
  updateControls();
}

function reportProgress(value, ticket) {
  if (ticket !== generation || phase !== 'loading') return;
  let percent;
  if (typeof value === 'number') percent = value;
  else if (typeof value === 'string') setStatus(value);
  else if (value && typeof value === 'object') {
    if (typeof value.message === 'string') setStatus(value.message);
    if (Number.isFinite(value.percent)) percent = value.percent;
    else if (Number.isFinite(value.current) && value.total > 0) percent = value.current / value.total * 100;
  }
  if (Number.isFinite(percent)) el('progress').value = Math.max(0, Math.min(100, percent));
  else el('progress').removeAttribute('value');
}

function showPreview() {
  if (!result) return;
  const selected = el('chapter-select').value;
  const text = selected === 'all' ? result.text : result.chapters[Number(selected)]?.text || '';
  el('preview').textContent = text.slice(0, PREVIEW_LIMIT);
  el('preview').scrollTop = 0;
  el('preview-note').textContent = text.length > PREVIEW_LIMIT
    ? '미리보기에는 앞부분만 표시합니다. 다운로드 파일에는 변환된 전체 본문이 포함됩니다.'
    : '다운로드 파일에는 선택한 미리보기 범위와 관계없이 변환된 전체 본문이 포함됩니다.';
}

function renderResult() {
  el('book-title').textContent = result.title || source.name;
  el('book-author').textContent = result.author || '';
  el('book-author').hidden = !result.author;
  el('book-format').textContent = String(result.format || '').toUpperCase();
  el('section-count-label').textContent = String(result.format).toLowerCase() === 'pdf' ? '페이지' : '본문 구간';
  el('chapter-count').textContent = result.chapters.length.toLocaleString('ko-KR');
  let characters = 0;
  for (const _ of result.text) characters += 1;
  el('character-count').textContent = characters.toLocaleString('ko-KR');
  el('source-size').textContent = formatBytes(source.size);
  el('chapter-select').replaceChildren(new Option('전체 본문', 'all'));
  for (let index = 0; index < result.chapters.length; index += 1) {
    const chapter = result.chapters[index];
    el('chapter-select').append(new Option(`${index + 1}. ${chapter.title || '본문'}`, String(index)));
  }
  el('warning-list').replaceChildren();
  for (const warning of result.warnings || []) {
    const item = document.createElement('li');
    item.textContent = typeof warning === 'string' ? warning : warning.message || String(warning);
    el('warning-list').append(item);
  }
  el('warnings').hidden = !(result.warnings || []).length;
  el('result').hidden = false;
  showPreview();
}

async function selectFile(files) {
  if (!files.length) return;
  if (files.length !== 1) {
    failSelection('한 번에 책 파일 한 권을 선택하세요. 이전 변환 결과는 지웠습니다.');
    return;
  }
  const ticket = ++generation;
  active?.abort();
  const controller = new AbortController();
  active = controller;
  source = files[0];
  phase = 'loading';
  clearResult();
  el('file-name').textContent = `${source.name} · ${formatBytes(source.size)}`;
  el('file-name').hidden = false;
  el('progress').removeAttribute('value');
  setStatus('파일 형식과 본문을 확인하고 있습니다.');
  updateControls();
  try {
    const book = await extractBook(source, {
      signal: controller.signal,
      onProgress: value => reportProgress(value, ticket),
      pdfOptions,
    });
    if (ticket !== generation) return;
    if (controller.signal.aborted) throw new DOMException('변환 취소', 'AbortError');
    if (!book || typeof book.text !== 'string' || !book.text.trim() || !Array.isArray(book.chapters)) {
      throw new Error('변환된 본문이 없습니다. 파일의 형식과 텍스트 포함 여부를 확인하세요.');
    }
    result = book;
    phase = 'ready';
    renderResult();
    setStatus('본문 변환을 마쳤습니다. 미리보기를 확인하고 원하는 형식으로 저장하세요.', 'success');
  } catch (error) {
    if (ticket !== generation) return;
    clearResult();
    phase = 'error';
    if (controller.signal.aborted || error?.name === 'AbortError') {
      setStatus('변환을 취소했습니다. 다른 파일을 선택하거나 같은 파일을 다시 선택할 수 있습니다.');
    } else {
      setStatus(error?.message || '이 파일을 처리하지 못했습니다. 다른 파일을 선택하세요.', 'error');
    }
  } finally {
    if (ticket === generation) {
      active = null;
      updateControls();
    }
  }
}

function filename(extension) {
  const base = (result?.title || source?.name || 'book').replace(/\.(epub|pdf|txt)$/i, '')
    .replace(/[\u0000-\u001f\u007f<>:"/\\|?*]/g, '_').replace(/^\.+|[. ]+$/g, '').trim();
  return `${Array.from(base || 'book').slice(0, 100).join('')}.${extension}`;
}

function markdownText(text) {
  return String(text).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/[\\`*_[\]]/g, '\\$&').replace(/^(\s*)([#>+-]|\d+[.)])(?=\s)/gm, '$1\\$2');
}

function download(kind) {
  if (phase !== 'ready' || !result) return;
  let content;
  let mime;
  if (kind === 'txt') {
    content = '\uFEFF' + result.text;
    mime = 'text/plain;charset=utf-8';
  } else if (kind === 'md') {
    content = `# ${markdownText(result.title || source.name).replace(/\r?\n/g, ' ')}\n\n`;
    if (result.author) content += `${markdownText(result.author).replace(/\r?\n/g, ' ')}\n\n`;
    content += result.chapters.map((chapter, index) => `## ${markdownText(chapter.title || `본문 ${index + 1}`).replace(/\r?\n/g, ' ')}\n\n${markdownText(chapter.text)}`).join('\n\n');
    content += '\n';
    mime = 'text/markdown;charset=utf-8';
  } else {
    content = JSON.stringify({
      formatVersion: 1,
      title: result.title,
      author: result.author || '',
      sourceFormat: result.format,
      sourceFile: { name: source.name, bytes: source.size },
      text: result.text,
      chapters: result.chapters.map((chapter, index) => ({ index, title: chapter.title || '', text: chapter.text })),
      warnings: result.warnings || [],
    }, null, 2);
    mime = 'application/json;charset=utf-8';
  }
  const blob = new Blob([content], { type: mime });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = filename(kind);
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60000);
  setStatus(`${kind.toUpperCase()} 파일 다운로드를 요청했습니다. 브라우저의 다운로드 목록을 확인하세요.`, 'success');
}

el('choose-book').addEventListener('click', () => el('book-file').click());
el('book-file').addEventListener('change', event => {
  const files = Array.from(event.target.files || []);
  event.target.value = '';
  selectFile(files);
});
el('cancel').addEventListener('click', () => {
  if (!active) return;
  active.abort();
  el('cancel').disabled = true;
  setStatus('변환을 취소하고 있습니다.');
});
el('reset').addEventListener('click', reset);
el('chapter-select').addEventListener('change', showPreview);
for (const kind of ['txt', 'md', 'json']) el(`download-${kind}`).addEventListener('click', () => download(kind));

let dragDepth = 0;
el('drop-zone').addEventListener('dragenter', event => {
  event.preventDefault();
  dragDepth += 1;
  el('drop-zone').classList.add('dragging');
});
el('drop-zone').addEventListener('dragover', event => {
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy';
});
el('drop-zone').addEventListener('dragleave', event => {
  event.preventDefault();
  dragDepth = Math.max(0, dragDepth - 1);
  if (!dragDepth) el('drop-zone').classList.remove('dragging');
});
el('drop-zone').addEventListener('drop', event => {
  event.preventDefault();
  dragDepth = 0;
  el('drop-zone').classList.remove('dragging');
  const files = Array.from(event.dataTransfer?.files || []);
  if (!files.length) failSelection('폴더나 링크 대신 EPUB·PDF·TXT 파일 한 권을 놓아주세요.');
  else selectFile(files);
});
for (const eventName of ['dragover', 'drop']) window.addEventListener(eventName, event => {
  if (Array.from(event.dataTransfer?.types || []).includes('Files')) event.preventDefault();
});
reset();
