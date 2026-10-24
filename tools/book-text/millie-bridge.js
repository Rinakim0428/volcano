const MAX_BOOK_BYTES = 128 * 1024 * 1024;

const stateMessages = {
  app_not_running: 'Mac에서 밀리의서재 앱을 실행하고 원하는 책을 연 뒤 다시 연결하세요.',
  app_unverified: '실행 중인 앱을 밀리의서재로 확인하지 못했습니다. 공식 Mac 앱을 실행한 뒤 다시 연결하세요.',
  accessibility_required: '열어 둔 책을 확인하려면 macOS의 손쉬운 사용 권한이 필요합니다. macOS가 안내하는 권한을 허용한 뒤 다시 연결하세요.',
  reader_not_found: '열려 있는 책을 찾지 못했습니다. 밀리 앱에서 원하는 책의 본문을 연 뒤 다시 연결하세요.',
  open_reader_required: '밀리 앱에서 원하는 책의 본문을 연 뒤 다시 연결하세요.',
  no_local_server: '밀리 앱의 본문 읽기 기능을 아직 찾지 못했습니다. 책의 본문이 표시된 상태에서 다시 연결하세요.',
  unsupported_platform: '밀리 연결은 Mac에서 실행한 연결 도구에서 사용할 수 있습니다.',
  ready_to_probe: '열린 책의 연결 정보는 찾았지만, 밀리 앱의 응답에서 읽기 가능한 본문을 확인하지 못했습니다.',
  blocked: '밀리 앱이 외부 도구의 본문 읽기를 허용하지 않았습니다. 본문을 가져오지 못했습니다.',
  access_denied: '밀리 앱이 외부 도구의 본문 읽기를 허용하지 않았습니다. 본문을 가져오지 못했습니다.',
  forbidden: '밀리 앱이 외부 도구의 본문 읽기를 허용하지 않았습니다. 본문을 가져오지 못했습니다.',
  unauthorized: '연결 세션이 유효하지 않습니다. Mac 연결 도구를 다시 실행하세요.',
  invalid_token: '연결 세션이 유효하지 않습니다. Mac 연결 도구를 다시 실행하세요.',
  target_changed: '열린 책이나 앱 상태가 바뀌었습니다. 다시 연결해서 책을 확인하세요.',
  no_readable_text: '현재 열린 책에서 읽을 수 있는 텍스트 본문을 찾지 못했습니다.',
  encrypted: '보호된 본문은 가져올 수 없습니다.',
  too_large: '본문 파일이 최대 크기인 128 MiB를 초과했습니다.',
};

// The launcher provides a short-lived session in the fragment. It is never
// persisted, exposed as a global, or sent to the reader's own HTTP service.
export function initMillieBridge({ onFile }) {
  if (location.protocol !== 'http:' || location.hostname !== '127.0.0.1') return;
  if (!/^#millie=[a-f0-9]{64}$/.test(location.hash)) {
    if (document.querySelector('meta[name="millie-bridge-host"][content="1"]')) {
      const expiredPanel = document.getElementById('millie-panel');
      const expiredStatus = document.getElementById('millie-status');
      const expiredConnect = document.getElementById('millie-connect');
      if (expiredPanel && expiredStatus && expiredConnect) {
        expiredPanel.hidden = false;
        expiredConnect.disabled = true;
        expiredStatus.className = 'status error';
        expiredStatus.textContent = '연결 세션이 끝났습니다. Mac 연결 프로그램을 종료한 뒤 다시 실행하세요.';
      }
    }
    return;
  }
  const token = location.hash.slice('#millie='.length);
  history.replaceState(history.state, '', location.pathname + location.search);

  const panel = document.getElementById('millie-panel');
  const connect = document.getElementById('millie-connect');
  const status = document.getElementById('millie-status');
  const books = document.getElementById('millie-books');
  const cancel = document.getElementById('millie-cancel');
  const notes = document.getElementById('millie-notes');
  if (!panel || !connect || !status || !books || !cancel || !notes || typeof onFile !== 'function') return;
  panel.hidden = false;
  let generation = 0;
  let active = null;

  function setStatus(message, kind = '') {
    status.textContent = message;
    status.className = `status ${kind}`.trim();
  }

  function setBusy(busy) {
    connect.disabled = busy;
    cancel.hidden = !busy;
    panel.setAttribute('aria-busy', String(busy));
    for (const button of books.querySelectorAll('button')) button.disabled = busy;
  }

  function begin(message) {
    active?.abort();
    const operation = { ticket: ++generation, controller: new AbortController() };
    active = operation.controller;
    setBusy(true);
    setStatus(message);
    return operation;
  }

  function current(operation) {
    return operation.ticket === generation && !operation.controller.signal.aborted;
  }

  function finish(operation) {
    if (operation.ticket !== generation) return;
    active = null;
    setBusy(false);
  }

  function userMessage(value, fallback) {
    if (typeof value !== 'string' || !value.trim()) return fallback;
    // Never echo the session even if an unexpected server error includes it.
    return value.replaceAll(token, '[숨김]').slice(0, 700);
  }

  async function readBody(response, limit) {
    const declared = Number(response.headers.get('content-length'));
    if (Number.isFinite(declared) && declared > limit) {
      await response.body?.cancel();
      throw new Error(limit === MAX_BOOK_BYTES ? stateMessages.too_large : '연결 도구의 응답이 너무 큽니다.');
    }
    if (!response.body) throw new Error('연결 도구가 빈 응답을 보냈습니다.');
    const reader = response.body.getReader();
    const chunks = [];
    let size = 0;
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        size += value.byteLength;
        if (size > limit) {
          await reader.cancel();
          throw new Error(limit === MAX_BOOK_BYTES ? stateMessages.too_large : '연결 도구의 응답이 너무 큽니다.');
        }
        chunks.push(value);
      }
    } finally {
      reader.releaseLock();
    }
    return new Blob(chunks, { type: response.headers.get('content-type') || 'application/octet-stream' });
  }

  async function post(path, data, operation) {
    let response;
    try {
      response = await fetch(path, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
        body: JSON.stringify(data),
        cache: 'no-store',
        credentials: 'omit',
        redirect: 'error',
        referrerPolicy: 'no-referrer',
        signal: operation.controller.signal,
      });
    } catch (error) {
      if (operation.controller.signal.aborted) throw error;
      throw new Error('Mac 연결 도구에 연결하지 못했습니다. 연결 도구가 실행 중인지 확인하세요.');
    }
    if (!response.ok) {
      let detail;
      try { detail = JSON.parse(await (await readBody(response, 64 * 1024)).text()).error; } catch { /* Use the safe fallback below. */ }
      throw new Error(stateMessages[detail?.code] || userMessage(detail?.message, '본문을 가져오지 못했습니다. 밀리 앱에서 책을 연 뒤 다시 연결하세요.'));
    }
    return response;
  }

  function showNotes(data) {
    notes.replaceChildren();
    const messages = [
      ...(Array.isArray(data.warnings) ? data.warnings : []),
      ...(Array.isArray(data.issues) ? data.issues.map(issue => stateMessages[issue?.code] || issue?.message) : []),
    ];
    for (const message of [...new Set(messages)].slice(0, 12)) {
      if (typeof message !== 'string' || !message.trim()) continue;
      const li = document.createElement('li');
      li.textContent = userMessage(message, '');
      notes.append(li);
    }
    notes.hidden = !notes.childElementCount;
  }

  async function importBook(book) {
    const operation = begin('현재 열린 책에서 읽을 수 있는 본문을 가져오고 있습니다.');
    try {
      const response = await post('/api/import', book.target, operation);
      if (!response.headers.get('content-type')?.toLowerCase().startsWith('application/epub+zip')) {
        await response.body?.cancel();
        throw new Error('본문 파일 형식이 올바르지 않습니다. 다시 연결하세요.');
      }
      const blob = await readBody(response, MAX_BOOK_BYTES);
      if (!current(operation)) return;
      if (!blob.size) throw new Error('가져온 본문 파일이 비어 있습니다.');
      const name = (String(book.title || '밀리에서 가져온 본문').replace(/[<>:"/\\|?*\u0000-\u001f]/g, '_').slice(0, 100) || '밀리에서 가져온 본문') + '.epub';
      const file = new File([blob], name, { type: 'application/epub+zip' });
      finish(operation);
      setStatus('본문 파일을 가져와 변환을 시작했습니다. 아래에서 변환 결과와 실제 본문 범위를 확인한 뒤 저장하세요. 미리보기 일부만 포함될 수 있습니다.');
      // selectFile owns conversion progress, cancellation, and validation.
      // Receiving a file does not establish that the entire book was imported.
      await onFile(file);
    } catch (error) {
      if (current(operation)) setStatus(userMessage(error.message, '본문을 가져오지 못했습니다.'), 'error');
    } finally {
      finish(operation);
    }
  }

  function renderBooks(items) {
    books.replaceChildren();
    for (const book of items) {
      const item = document.createElement('li');
      item.className = 'millie-book';
      const description = document.createElement('div');
      const title = document.createElement('strong');
      title.textContent = userMessage(book.title, '열어 둔 책');
      description.append(title);
      const details = [];
      if (typeof book.author === 'string' && book.author.trim()) details.push(userMessage(book.author, ''));
      const count = Array.isArray(book.chapters) ? book.chapters.length : book.chapters;
      if (Number.isSafeInteger(count) && count > 0) details.push(`목차 항목 ${count}개`);
      if (details.length) {
        const meta = document.createElement('p');
        meta.textContent = details.join(' · ');
        description.append(meta);
      }
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'primary';
      button.textContent = '본문 가져오기';
      button.addEventListener('click', () => importBook(book));
      item.append(description, button);
      books.append(item);
    }
    books.hidden = !items.length;
  }

  connect.addEventListener('click', async () => {
    const operation = begin('Mac 밀리 앱에서 열어 둔 책을 확인하고 있습니다. macOS의 권한 안내가 나타날 수 있습니다.');
    books.replaceChildren();
    books.hidden = true;
    notes.replaceChildren();
    notes.hidden = true;
    try {
      const response = await post('/api/discover', {}, operation);
      const data = JSON.parse(await (await readBody(response, 1024 * 1024)).text());
      if (!current(operation)) return;
      if (!data || typeof data !== 'object') throw new Error('연결 도구의 응답을 읽지 못했습니다.');
      const readable = (Array.isArray(data.books) ? data.books : []).filter(book => book && book.target && Number.isSafeInteger(book.target.port) && book.target.port > 0 && book.target.port <= 65535 && typeof book.target.bookSeq === 'string' && book.target.bookSeq.length > 0);
      renderBooks(readable);
      showNotes(data);
      if (readable.length) {
        setStatus(`열린 책 ${readable.length}권의 정보를 확인했습니다. ‘본문 가져오기’를 눌러 실제 본문을 읽을 수 있는지 확인하세요.`, 'success');
      } else {
        setStatus(stateMessages[data.state] || '현재 읽기 가능한 본문을 확인하지 못했습니다. 밀리 앱에서 책 본문을 연 뒤 다시 연결하세요.', 'error');
      }
    } catch (error) {
      if (current(operation)) setStatus(userMessage(error.message, '밀리 앱 연결을 확인하지 못했습니다.'), 'error');
    } finally {
      finish(operation);
      if (operation.ticket === generation) connect.textContent = '다시 연결 확인';
    }
  });

  cancel.addEventListener('click', () => {
    ++generation;
    active?.abort();
    active = null;
    setBusy(false);
    setStatus('연결 또는 본문 가져오기를 취소했습니다. 필요한 경우 다시 시도하세요.');
  });
}
