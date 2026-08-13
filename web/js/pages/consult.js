function renderSessionList(filter) {
  const container = $('#session-list');
  const emptyEl = $('#sessions-empty');
  let sessions = state.sessions;
  if (filter) {
    const q = filter.toLowerCase();
    sessions = sessions.filter(s => {
      if (s.title.toLowerCase().includes(q)) return true;
      return s.messages.some(m => m.content && m.content.toLowerCase().includes(q));
    });
  }
  if (sessions.length === 0) {
    container.innerHTML = '';
    emptyEl.style.display = 'flex';
    return;
  }
  emptyEl.style.display = 'none';
  container.innerHTML = sessions.map(s => {
    // Preview prefers the last ASSISTANT reply — that's the "answer" the user wants to recognize.
    let answerMsg = null;
    for (let i = s.messages.length - 1; i >= 0; i--) {
      if (s.messages[i].role === 'assistant' && s.messages[i].content) { answerMsg = s.messages[i]; break; }
    }
    const lastMsg = s.messages.length > 0 ? s.messages[s.messages.length - 1] : null;
    const src = answerMsg || lastMsg;
    const preview = src && typeof src.content === 'string'
      ? src.content.replace(/\s*\n\s*/g, ' ').slice(0, 300) : '';
    return `
      <div class="session-item" data-id="${escHtml(s.id)}">
        <div class="session-content">
          <div class="session-title">${escHtml(s.title)}</div>
          ${preview ? `<div class="session-preview">${escHtml(preview)}</div>` : ''}
          <div class="session-meta">
            <span>${formatTime(s.updatedAt)}</span>
            <span>${s.messages.length} messages</span>
          </div>
        </div>
        <div class="swipe-bg"><span>Delete</span></div>
      </div>`;
  }).join('');
}

function escHtml(s) {
  // Escape quotes too: textContent alone doesn't, leaving attribute-injection XSS on server-derived data.
  if (s === null || s === undefined) return '';
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function jsStr(s) {
  // escHtml's &#39; isn't a JS quote — only raw ' closes the literal, so backslash-escape it (XSS).
  return String(s === null || s === undefined ? '' : s)
    .replace(/\\/g, '\\\\')
    .replace(/'/g, "\\'")
    .replace(/"/g, '\\"')
    .replace(/\n/g, '\\n')
    .replace(/\r/g, '\\r')
    .replace(/\x00/g, '\\0');
}

// " ] in a querySelector attribute would throw; manual fallback for old WebViews.
function cssAttrEscape(v) {
  const s = String(v);
  if (window.CSS && CSS.escape) return CSS.escape(s);
  return s.replace(/["\\\]]/g, '\\$&');
}

function initSessionListTouch() {
  const list = $('#session-list');
  let startX = 0, startY = 0, item = null, swiping = false;

  list.addEventListener('touchstart', e => {
    item = e.target.closest('.session-item');
    if (!item) return;
    startX = e.touches[0].clientX;
    startY = e.touches[0].clientY;
    swiping = false;

    clearTimeout(state.pressTimer);
    state.longPressTarget = item;
    state.pressTimer = setTimeout(() => {
      const id = item.dataset.id;
      openRenameModal(id);
    }, 600);
  }, { passive: true });

  list.addEventListener('touchmove', e => {
    if (!item) return;
    const dx = e.touches[0].clientX - startX;
    const dy = e.touches[0].clientY - startY;
    if (Math.abs(dx) > Math.abs(dy) && Math.abs(dx) > 5) {
      swiping = true;
      clearTimeout(state.pressTimer);
      if (dx < -30) {
        item.classList.add('swiped');
      } else if (dx > 10) {
        item.classList.remove('swiped');
      }
    } else if (Math.abs(dy) > 10) {
      clearTimeout(state.pressTimer);
    }
  }, { passive: true });

  list.addEventListener('touchend', e => {
    clearTimeout(state.pressTimer);
    if (!item) return;
    if (swiping && item.classList.contains('swiped')) {
      const touch = e.changedTouches[0];
      if (touch) {
        const el = document.elementFromPoint(touch.clientX, touch.clientY);
        if (el && el.closest('.swipe-bg')) {
          state._suppressClick = true;
          // Auto-clear: some WebViews skip the click after a big swipe, and a stuck flag would swallow the next tap.
          setTimeout(function() { state._suppressClick = false; }, 400);
          // FE-18: mark as swipe-deleted to prevent double-trigger
          item.dataset.swipeDeleted = '1';
          deleteSession(item.dataset.id);
          renderSessionList();
          showToast('Consult deleted');
          if (state.currentSessionId === item.dataset.id) {
            state.currentSessionId = null;
            showPage('consult');
          }
        }
      }
    } else if (!swiping) {
      const touch = e.changedTouches[0];
      if (touch && item.classList.contains('swiped')) {
        const el = document.elementFromPoint(touch.clientX, touch.clientY);
        if (el && el.closest('.swipe-bg')) {
          state._suppressClick = true;
          // Auto-clear: some WebViews skip the click after a big swipe, and a stuck flag would swallow the next tap.
          setTimeout(function() { state._suppressClick = false; }, 400);
          // FE-18: mark as swipe-deleted to prevent double-trigger
          item.dataset.swipeDeleted = '1';
          deleteSession(item.dataset.id);
          renderSessionList();
          showToast('Consult deleted');
          if (state.currentSessionId === item.dataset.id) {
            state.currentSessionId = null;
            showPage('consult');
          }
          item = null;
          return;
        }
      }
    }
    item = null;
  });
}

$('#session-list').addEventListener('click', e => {
  if (state._suppressClick) { state._suppressClick = false; return; }
  const item = e.target.closest('.session-item');
  if (!item) return;
  // FE-18: skip click if item was already deleted by swipe
  if (item.dataset.swipeDeleted) return;
  if (item.classList.contains('swiped')) {
    if (e.target.closest('.swipe-bg')) {
      deleteSession(item.dataset.id);
      renderSessionList();
      showToast('Consult deleted');
      if (state.currentSessionId === item.dataset.id) {
        state.currentSessionId = null;
        showPage('consult');
      }
    } else {
      item.classList.remove('swiped');
    }
    return;
  }
  openSession(item.dataset.id);
});

function openSession(id) {
  state.currentSessionId = id;
  // FE-3: persist last session
  try { localStorage.setItem('openyou_last_session', id); } catch(e) {}
  const s = getSession(id);
  if (s) {
    $('#chat-title').textContent = s.title;
  }
  // Force 'new' sub-tab — clicking a History entry must show chat, not the history view again.
  try { localStorage.setItem('openyou_last_csub', 'new'); } catch(e) {}
  switchTab('consult');
  ensureWebSocket();
  syncSessionFromServer(id);
}

// SQLite is the source of truth: re-hydrates a session even after localStorage is cleared.
function syncSessionFromServer(id) {
  fetch('/api/sessions/' + encodeURIComponent(id))
    .then(r => r.ok ? r.json() : null)
    .then(data => {
      if (!data || !data.id) return;
      const serverMsgs = (data.messages || []).map(m => ({
        id: m.id || genId(),
        role: m.role,
        content: m.content,
        time: m.created_at ? Math.round(m.created_at / 1e6) : ts(),
      }));
      if (serverMsgs.length === 0) return;
      let sess = getSession(id);
      if (!sess) {
        sess = {
          id: data.id,
          title: data.title || 'Consult',
          messages: [],
          createdAt: data.created_at ? Math.round(data.created_at / 1e6) : ts(),
          updatedAt: data.updated_at ? Math.round(data.updated_at / 1e6) : ts(),
        };
        state.sessions.unshift(sess);
        saveSessions();
      }
      // Skip while streaming: re-render would replace the bubble mid-stream and drop in-flight text.
      if (state.isStreaming && id === state.streamingSessionId) return;
      if (serverMsgs.length > sess.messages.length) {
        sess.messages = serverMsgs;
        sess.title = data.title || sess.title;
        saveSessions();
        if (state.currentSessionId === id) {
          renderChatMessages();
          scrollChatBottom(state.isStreaming /* force during streaming */);
        }
      }
    })
    .catch(() => {}); // daemon offline — use local cache silently
}

function openRenameModal(id) {
  const s = getSession(id);
  if (!s) return;
  $('#rename-input').value = s.title;
  state._renameTargetId = id;
  openModal('modal-rename');
}

$('#session-search').addEventListener('input', function() {
  renderSessionList(this.value);
});

function renderChatMessages() {
  const s = getSession(state.currentSessionId);
  const container = $('#chat-messages');
  if (!s || s.messages.length === 0) {
    container.innerHTML = '';
    const oldNotice = $('#chat-compaction-notice');
    if (oldNotice) oldNotice.remove();
    return;
  }
  container.innerHTML = s.messages.map(m => renderMessageHTML(m)).join('');
  container.querySelectorAll('.msg.assistant .msg-bubble').forEach(bubble => setupMessageCollapse(bubble));
  const oldNotice = $('#chat-compaction-notice');
  if (oldNotice) oldNotice.remove();
  if (s.messages.length > 30) {
    const chatHeader = $('#consult-new').querySelector('.session-title-area');
    if (chatHeader) {
      const notice = document.createElement('div');
      notice.id = 'chat-compaction-notice';
      notice.style.cssText = 'padding:4px 12px;font-size:var(--fs-sm);background:var(--accent-bg);color:var(--accent);text-align:center';
      notice.textContent = 'Earlier conversation summarized.';
      chatHeader.after(notice);
    }
  }
}

function renderMessageHTML(m) {
  // Server-sourced ids land in attributes — a raw " would close the attribute early.
  let html = `<div class="msg ${m.role}" data-id="${escHtml(m.id)}" data-role="${m.role}">`;
  const contentHtml = m.role === 'assistant'
    ? renderCitations(formatContent(m.content))
    : formatContent(m.content);
  html += `<div class="msg-bubble">${contentHtml}`;

  if (m.tools && m.tools.length > 0) {
    m.tools.forEach(t => {
      html += `<div class="tool-card">
        <span class="tool-label">${escHtml(t.tool || t.name || 'tool')}</span>
        ${t.args ? `<div class="tool-args">${escHtml(typeof t.args === 'string' ? t.args : JSON.stringify(t.args))}</div>` : ''}
        ${t.output ? `<div class="tool-output">${escHtml(typeof t.output === 'string' ? t.output.slice(0, 500) : '')}</div>` : ''}
        ${t.error ? `<div class="tool-output" style="color:var(--danger)">Error: ${escHtml(t.error)}</div>` : ''}
      </div>`;
    });
  }

  if (m.citations && m.citations.length > 0) {
    html += `<div class="citations"><div class="citation-label">Sources:</div>`;
    var max = 3;
    m.citations.slice(0, max).forEach(c => {
      html += `<div class="citation-item cite-${escHtml(c.from || 'person')}" title="${escHtml(c.url || c.source || '')}" role="button" tabindex="0">${escHtml(c.title || c.source || c.url || '')}</div>`;
    });
    if (m.citations.length > max) {
      html += `<div class="citation-item" style="color:var(--text-hint);font-style:italic" role="button" tabindex="0">... +${m.citations.length - max} more</div>`;
    }
    html += `</div>`;
  }

  html += `</div>`;
  // onclick needs jsStr inside escHtml — either alone leaves an injection path.
  if (m.role === 'assistant' && m.content) {
    const sidAttr = escHtml(jsStr(state.currentSessionId));
    const midAttr = escHtml(jsStr(m.id));
    html += `<div class="feedback-btns" data-msg-id="${escHtml(m.id)}" data-session="${escHtml(state.currentSessionId)}">`;
    html += `<button class="fb-btn" data-msg-id="${escHtml(m.id)}" onclick="sendFeedback('${sidAttr}','${midAttr}','useful')" title="Useful">👍</button>`;
    html += `<button class="fb-btn" data-msg-id="${escHtml(m.id)}" onclick="sendFeedback('${sidAttr}','${midAttr}','inaccurate')" title="Inaccurate — add correction">👎</button>`;
    html += `</div>`;
  }
  html += `<div class="msg-time">${formatTime(m.time)}${m.interrupted ? ' (interrupted)' : ''}</div>`;
  html += `</div>`;
  return html;
}

function renderCitations(html) {
  return html
    .replace(/\[👤([^\]]+)\]/g, '<span class="cite-chip cite-person">👤$1</span>')
    .replace(/\[📋([^\]]+)\]/g, '<span class="cite-chip cite-rule">📋$1</span>')
    .replace(/\[🧠([^\]]+)\]/g, '<span class="cite-chip cite-skill">🧠$1</span>')
    .replace(/\[🌐([^\]]+)\]/g, '<span class="cite-chip cite-external">🌐$1</span>');
}

function formatContent(text) {
  if (!text) return '';
  let s = escHtml(text);
  // Inline code first: **/* inside a code span must not be parsed as bold/italic; restore after.
  const codeSpans = [];
  s = s.replace(/`([^`]+)`/g, (_, code) => {
    const i = codeSpans.length;
    codeSpans.push(code);
    return '\u0000CODE' + i + '\u0000';
  });
  s = s.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
  s = s.replace(/\*(.+?)\*/g, '<em>$1</em>');
  s = s.replace(/\n/g, '<br>');
  // Restore placeholders (content already escaped by escHtml above).
  s = s.replace(/\u0000CODE(\d+)\u0000/g, (_, i) =>
    '<code style="font-family:var(--font-mono);font-size:var(--fs-md);background:var(--bg-input);padding:1px 5px;border-radius:var(--radius-xs);">' + codeSpans[+i] + '</code>');
  return s;
}

// Security: escape text first, then wrap fixed markup only — document content
// can't inject HTML; links stay non-live because documents are user-editable data.
function renderMarkdownDoc(text) {
  if (!text) return '';
  var s = String(text);
  // Drop YAML frontmatter (skill docs start with ---/name/triggers/---); it's metadata, not reading content.
  s = s.replace(/^---[ \t]*\r?\n[\s\S]*?\r?\n---[ \t]*\r?\n?/, '');
  s = escHtml(s);

  // Fenced code blocks → placeholders so no other rule touches them.
  var codeBlocks = [];
  s = s.replace(/```[^\n]*\n([\s\S]*?)```/g, function(_, code) {
    codeBlocks.push(code.replace(/\n$/, ''));
    return '\u0001CB' + (codeBlocks.length - 1) + '\u0001';
  });
  var inlineCodes = [];
  s = s.replace(/`([^`\n]+)`/g, function(_, c) {
    inlineCodes.push(c);
    return '\u0001IC' + (inlineCodes.length - 1) + '\u0001';
  });

  function inline(t) {
    t = t.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
    t = t.replace(/(^|[^*])\*([^*\n]+)\*/g, '$1<em>$2</em>');
    t = t.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<span class="md-link">$1</span>');
    return t;
  }

  var lines = s.split('\n');
  var html = '';
  var i = 0;
  var isBlockStart = function(l) {
    var t = l.trim();
    return t === '' ||
      /^\u0001CB\d+\u0001$/.test(t) ||
      /^#{1,6}\s+/.test(t) ||
      /^(-{3,}|\*{3,}|_{3,})$/.test(t) ||
      /^\|.*\|\s*$/.test(t) ||
      /^&gt;/.test(t) ||
      /^\s*[-*+]\s+/.test(l) ||
      /^\s*\d+[.)]\s+/.test(l);
  };

  while (i < lines.length) {
    var line = lines[i];
    var trimmed = line.trim();
    if (trimmed === '') { i++; continue; }

    var cb = trimmed.match(/^\u0001CB(\d+)\u0001$/);
    if (cb) {
      html += '<pre class="md-pre"><code>' + codeBlocks[+cb[1]] + '</code></pre>';
      i++; continue;
    }

    var h = trimmed.match(/^(#{1,6})\s+(.*)$/);
    if (h) {
      var lvl = Math.min(h[1].length, 4);
      html += '<h' + lvl + ' class="md-h">' + inline(h[2]) + '</h' + lvl + '>';
      i++; continue;
    }

    if (/^(-{3,}|\*{3,}|_{3,})$/.test(trimmed)) { html += '<hr class="md-hr">'; i++; continue; }

    if (/^\|.*\|\s*$/.test(trimmed) && i + 1 < lines.length &&
        /^\|[\s:|-]+\|\s*$/.test(lines[i + 1].trim())) {
      var parseRow = function(r) {
        return r.replace(/^\||\|$/g, '').split('|').map(function(c) { return inline(c.trim()); });
      };
      var head = parseRow(trimmed);
      i += 2;
      var rows = [];
      while (i < lines.length && /^\|.*\|\s*$/.test(lines[i].trim())) {
        rows.push(parseRow(lines[i].trim()));
        i++;
      }
      html += '<div class="md-table-wrap"><table class="md-table"><thead><tr>' +
        head.map(function(c) { return '<th>' + c + '</th>'; }).join('') +
        '</tr></thead><tbody>' +
        rows.map(function(r) {
          return '<tr>' + r.map(function(c) { return '<td>' + c + '</td>'; }).join('') + '</tr>';
        }).join('') +
        '</tbody></table></div>';
      continue;
    }

    // '>' is '&gt;' after escaping.
    if (/^&gt;/.test(trimmed)) {
      var quote = [];
      while (i < lines.length && /^&gt;/.test(lines[i].trim())) {
        quote.push(inline(lines[i].trim().replace(/^&gt;\s?/, '')));
        i++;
      }
      html += '<blockquote class="md-quote">' + quote.join('<br>') + '</blockquote>';
      continue;
    }

    if (/^\s*[-*+]\s+/.test(line)) {
      var items = [];
      while (i < lines.length) {
        var m = lines[i].match(/^(\s*)[-*+]\s+(.*)$/);
        if (!m) break;
        var itemText = m[2].replace(/^\[[ xX]\]\s*/, function(box) {
          return box.toLowerCase().indexOf('x') >= 0 ? '☑ ' : '☐ ';
        });
        var indent = m[1].replace(/\t/g, '  ').length;
        if (indent >= 2 && items.length > 0) {
          var last = items[items.length - 1];
          last.sub = last.sub || [];
          last.sub.push(inline(itemText));
        } else {
          items.push({ text: inline(itemText) });
        }
        i++;
      }
      html += '<ul class="md-ul">' + items.map(function(it) {
        var sub = (it.sub && it.sub.length)
          ? '<ul>' + it.sub.map(function(x) { return '<li>' + x + '</li>'; }).join('') + '</ul>'
          : '';
        return '<li>' + it.text + sub + '</li>';
      }).join('') + '</ul>';
      continue;
    }

    if (/^\s*\d+[.)]\s+/.test(line)) {
      var oitems = [];
      while (i < lines.length) {
        var om = lines[i].match(/^\s*\d+[.)]\s+(.*)$/);
        if (!om) break;
        oitems.push(inline(om[1]));
        i++;
      }
      html += '<ol class="md-ol">' + oitems.map(function(x) { return '<li>' + x + '</li>'; }).join('') + '</ol>';
      continue;
    }

    var para = [inline(trimmed)];
    i++;
    while (i < lines.length && !isBlockStart(lines[i])) {
      para.push(inline(lines[i].trim()));
      i++;
    }
    html += '<p class="md-p">' + para.join('<br>') + '</p>';
  }

  html = html.replace(/\u0001CB(\d+)\u0001/g, function(_, n) {
    return '<pre class="md-pre"><code>' + codeBlocks[+n] + '</code></pre>';
  });
  html = html.replace(/\u0001IC(\d+)\u0001/g, function(_, n) {
    return '<code class="md-code">' + inlineCodes[+n] + '</code>';
  });
  return html;
}

// Read-first: saveBtn shows only in edit mode (nothing to save while reading).
function wireMarkdownPreview(btnId, taId, previewId, startInPreview, saveBtnId) {
  const btn = $('#' + btnId);
  if (!btn) return;
  const ta = $('#' + taId);
  const pv = $('#' + previewId);
  if (!ta || !pv) return;
  const saveBtn = saveBtnId ? $('#' + saveBtnId) : null;
  const showPreview = function() {
    pv.innerHTML = renderMarkdownDoc(ta.value);
    pv.style.display = '';
    ta.style.display = 'none';
    btn.textContent = 'Edit';
    if (saveBtn) saveBtn.style.display = 'none';
  };
  const showEditor = function() {
    pv.style.display = 'none';
    ta.style.display = '';
    btn.textContent = 'Preview';
    if (saveBtn) saveBtn.style.display = '';
  };
  btn.addEventListener('click', function() {
    if (pv.style.display !== 'none') showEditor(); else showPreview();
  });
  if (startInPreview) showPreview();
}

function setupEditorPreview(taId, previewId) {
  const btn = $('#btn-editor-preview');
  if (!btn) return;
  btn.textContent = 'Preview';
  btn.onclick = function() {
    const ta = $('#' + taId);
    const pv = $('#' + previewId);
    if (!ta || !pv) return;
    if (pv.style.display !== 'none') {
      pv.style.display = 'none';
      ta.style.display = '';
      btn.textContent = 'Preview';
    } else {
      pv.innerHTML = renderMarkdownDoc(ta.value);
      pv.style.display = '';
      ta.style.display = 'none';
      btn.textContent = 'Edit';
    }
  };
}

function appendMsgToDOM(m) {
  const container = $('#chat-messages');
  const div = document.createElement('div');
  div.innerHTML = renderMessageHTML(m);
  while (div.firstChild) container.appendChild(div.firstChild);
  if (m.role === 'assistant') {
    const msgs = container.querySelectorAll('.msg.assistant');
    if (msgs.length > 0) {
      const lastBubble = msgs[msgs.length - 1].querySelector('.msg-bubble');
      if (lastBubble) setupMessageCollapse(lastBubble);
    }
  }
  scrollChatBottom(state.isStreaming /* force during streaming */);
}

function setupMessageCollapse(bubble) {
  requestAnimationFrame(function() {
    if (bubble.scrollHeight > 600) {
      bubble.classList.add('overflowing');
      bubble.addEventListener('click', function() {
        this.classList.toggle('expanded');
        this.classList.remove('overflowing');
      }, {once: true});
    }
  });
}

function appendTextDelta(text) {
  const s = getSession(state.currentSessionId);
  if (!s) return;
  const bubbles = $$('.msg.assistant .msg-bubble', $('#chat-messages'));
  if (bubbles.length === 0) return;
  const lastBubble = bubbles[bubbles.length - 1];
  state.currentStreamingText = (state.currentStreamingText || '') + text;
  // Rebuild innerHTML only every 50 chars or on newlines — reduces layout thrashing.
  if (state.currentStreamingText.length % 50 === 0 || text.includes('\n')) {
    lastBubble.innerHTML = formatContent(state.currentStreamingText);
  } else {
    const textNode = document.createTextNode(text);
    lastBubble.appendChild(textNode);
  }
  const lastAssistant = s.messages[s.messages.length - 1];
  if (lastAssistant && lastAssistant.tools && lastAssistant.tools.length > 0) {
    lastAssistant.tools.forEach(t => {
      const card = document.createElement('div');
      card.className = 'tool-card';
      card.innerHTML = `<span class="tool-label">${escHtml(t.tool || t.name || 'tool')}</span>`;
      if (t.args) {
        const argsDiv = document.createElement('div');
        argsDiv.className = 'tool-args';
        argsDiv.textContent = typeof t.args === 'string' ? t.args : JSON.stringify(t.args);
        card.appendChild(argsDiv);
      }
      if (t.output) {
        const outputDiv = document.createElement('div');
        outputDiv.className = 'tool-output';
        outputDiv.textContent = (typeof t.output === 'string' ? t.output : '').slice(0, 500);
        card.appendChild(outputDiv);
      }
      lastBubble.appendChild(card);
    });
  }
  scrollChatBottom(state.isStreaming /* force during streaming */);
}

function showThinking(show) {
  $('#thinking-indicator').style.display = show ? 'flex' : 'none';
}

function setStreaming(isStreaming) {
  state.isStreaming = isStreaming;
  const input = $('#chat-input');
  const btn = $('#btn-send');
  btn.disabled = isStreaming;
  input.disabled = isStreaming;
  if (!isStreaming) {
    input.focus();
    $('#thinking-indicator').style.display = 'none';
    const bubbles = $$('.msg.assistant', $('#chat-messages'));
    bubbles.forEach(b => b.classList.remove('streaming'));
  } else {
    const bubbles = $$('.msg.assistant', $('#chat-messages'));
    if (bubbles.length > 0) bubbles[bubbles.length - 1].classList.add('streaming');
  }
}

function scrollChatBottom(force) {
  const container = $('#chat-messages');
  const threshold = 150;
  const atBottom = container.scrollHeight - container.scrollTop - container.clientHeight < threshold;
  if (atBottom || force) {
    container.scrollTop = container.scrollHeight;
  }
}

function sendMessage() {
  const input = $('#chat-input');
  const text = input.value.trim();
  if (!text || state.isStreaming) return;
  recordUsage('consult');
  if (!state.isConnected) {
    // Bind to the session it was typed in — a late flush after switching sessions must not hit the wrong one.
    state.pendingSendQueue.push({ text: text, sid: state.currentSessionId });
    showToast('Queued. Will send when connected.', 'warn');
    input.value = '';
    return;
  }
  if (!state.currentSessionId) {
    const s = createSession();
    state.currentSessionId = s.id;
  }

  const msg = addMessage(state.currentSessionId, 'user', text);
  appendMsgToDOM(msg);
  input.value = '';
  input.style.height = 'auto';

  state.lastUserMessage = text;
  // Bind the stream to its session so deltas/done never write elsewhere if the user switches mid-stream.
  state.streamingSessionId = state.currentSessionId;
  sendWS({ type: 'chat', session_id: state.currentSessionId, message: text });

  const assistantMsg = addMessage(state.currentSessionId, 'assistant', '', {
    tools: [],
    citations: []
  });
  appendMsgToDOM(assistantMsg);
  setStreaming(true);
  showThinking(true);
  scrollChatBottom(true);

  renderSessionList();
}

function flushSendQueue() {
  if (state.pendingSendQueue.length > 10) {
    state.pendingSendQueue = state.pendingSendQueue.slice(-10);
  }
  if (state.pendingSendQueue.length > 0 && state.isConnected && !state.isStreaming) {
    const input = $('#chat-input');
    const draft = input ? input.value : '';
    const entry = state.pendingSendQueue.shift();
    // Tolerate legacy plain-string entries from older queue states.
    const text = typeof entry === 'string' ? entry : entry.text;
    const sid = typeof entry === 'string' ? null : entry.sid;
    if (sid && getSession(sid)) state.currentSessionId = sid;
    if (input) input.value = text;
    sendMessage();
    if (input && draft) input.value = draft;
  }
}

function initChatTouch() {
  const container = $('#chat-messages');
  container.addEventListener('touchstart', e => {
    const msgEl = e.target.closest('.msg');
    if (!msgEl) return;
    clearTimeout(state.pressTimer);
    state.longPressTarget = msgEl;
    state.pressTimer = setTimeout(() => {
      showContextMenu(e, msgEl);
    }, 600);
  }, { passive: true });

  container.addEventListener('touchmove', () => {
    clearTimeout(state.pressTimer);
  });

  container.addEventListener('touchend', () => {
    clearTimeout(state.pressTimer);
  });

  container.addEventListener('scroll', () => {});
}

function showContextMenu(e, msgEl) {
  const menu = $('#context-menu');
  const backdrop = $('#context-menu-backdrop');
  menu.style.top = (e.touches ? e.touches[0].clientY : e.clientY) + 'px';
  menu.style.left = (e.touches ? e.touches[0].clientX : e.clientX) + 'px';
  menu.classList.add('show');
  menu._target = msgEl;
  if (backdrop) backdrop.style.display = 'block';

  requestAnimationFrame(() => {
    const rect = menu.getBoundingClientRect();
    const safeBottom = 34; // FE-12: iPhone home indicator safe area
    if (rect.bottom > window.innerHeight - safeBottom) menu.style.top = (window.innerHeight - rect.height - safeBottom - 10) + 'px';
    if (rect.right > window.innerWidth) menu.style.left = (window.innerWidth - rect.width - 10) + 'px';
    if (rect.left < 0) menu.style.left = '10px';
  });
}

function closeContextMenu() {
  $('#context-menu').classList.remove('show');
  const backdrop = $('#context-menu-backdrop');
  if (backdrop) backdrop.style.display = 'none';
}

$('#context-menu').addEventListener('click', function(e) {
  const action = e.target.closest('.menu-item') && e.target.closest('.menu-item').dataset.action;
  const msgEl = this._target;
  if (!action || !msgEl) return;
  const msgId = msgEl.dataset.id;
  handleMessageAction(action, msgId, msgEl);
  closeContextMenu();
});

document.addEventListener('DOMContentLoaded', function() {
  const backdrop = document.getElementById('context-menu-backdrop');
  if (backdrop) {
    backdrop.addEventListener('click', closeContextMenu);
    backdrop.addEventListener('touchstart', closeContextMenu, { passive: true });
  }
});

function handleMessageAction(action, msgId, msgEl) {
  const s = getSession(state.currentSessionId);
  if (!s) return;
  const idx = s.messages.findIndex(m => m.id === msgId);
  if (idx < 0) return;

  switch (action) {
    case 'copy':
      navigator.clipboard.writeText(s.messages[idx].content || '').then(() => {
        showToast('Copied');
      }).catch(() => {});
      break;
    case 'delete':
      s.messages.splice(idx, 1);
      saveSessions();
      renderChatMessages();
      scrollChatBottom(true);
      break;
    case 'regenerate':
      // Don't regenerate while streaming/offline — two streams would interleave into one bubble.
      if (state.isStreaming) { showToast('Reply in progress, please wait', 'warn'); break; }
      if (!state.isConnected) { showToast('Not connected, cannot regenerate', 'warn'); break; }
      let userMsg = null;
      for (let i = idx - 1; i >= 0; i--) {
        if (s.messages[i].role === 'user') { userMsg = s.messages[i]; break; }
      }
      if (userMsg) {
        s.messages.splice(idx, s.messages.length - idx);
        saveSessions();
        renderChatMessages();
        state.lastUserMessage = userMsg.content;
        state.streamingSessionId = state.currentSessionId;
        sendWS({ type: 'chat', session_id: state.currentSessionId, message: userMsg.content });
        const assistantMsg = addMessage(state.currentSessionId, 'assistant', '', { tools: [], citations: [] });
        appendMsgToDOM(assistantMsg);
        setStreaming(true);
        showThinking(true);
        scrollChatBottom(true);
      }
      break;
    case 'edit':
      const content = s.messages[idx].content;
      $('#chat-input').value = content;
      s.messages.splice(idx, 1);
      saveSessions();
      renderChatMessages();
      $('#chat-input').focus();
      break;
  }
}

$('#retry-banner').addEventListener('click', function() {
  this.classList.remove('show');
  if (state.isStreaming) { showToast('Reply in progress, please wait', 'warn'); return; }
  if (!state.isConnected) { showToast('Not connected, cannot retry', 'warn'); return; }
  if (!state.lastUserMessage || !state.currentSessionId) return;
  // Fresh placeholder + stream bind: deltas must not append to the interrupted
  // bubble, and a second stream must not fire while this one runs.
  state.streamingSessionId = state.currentSessionId;
  sendWS({ type: 'chat', session_id: state.currentSessionId, message: state.lastUserMessage });
  const assistantMsg = addMessage(state.currentSessionId, 'assistant', '', { tools: [], citations: [] });
  appendMsgToDOM(assistantMsg);
  setStreaming(true);
  showThinking(true);
  scrollChatBottom(true);
});

const chatInput = $('#chat-input');
chatInput.addEventListener('input', function() {
  this.style.height = 'auto';
  this.style.height = Math.min(this.scrollHeight, 120) + 'px';
  checkPersonMention(this.value);
  const len = this.value.length;
  const counter = $('#chat-char-counter');
  if (counter) {
    counter.textContent = len > 2000 ? len + '' : '';
    counter.style.color = len > 2000 ? 'var(--warning)' : '';
  }
});

chatInput.addEventListener('keydown', function(e) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    sendMessage();
  }
});

$('#btn-send').addEventListener('click', sendMessage);

// #btn-new-session does not exist in current HTML; guarded for safety.
var btnNewSession = $('#btn-new-session');
if (btnNewSession) btnNewSession.addEventListener('click', () => {
  const s = createSession();
  state.currentSessionId = s.id;
  $('#chat-title').textContent = s.title;
  showPage('consult');
  ensureWebSocket();
});

$('#btn-chat-new').addEventListener('click', () => {
  const s = createSession();
  state.currentSessionId = s.id;
  $('#chat-title').textContent = s.title;
  renderChatMessages();
  scrollChatBottom(state.isStreaming /* force during streaming */);
  ensureWebSocket();
});

$('#btn-open-settings-from-home').addEventListener('click', () => {
  // FE-7: record settings entry point
  state._settingsFrom = state.currentPage;
  showPage('settings');
});

// #btn-chat-back does not exist in current HTML; guarded for safety.
var btnChatBack = $('#btn-chat-back');
if (btnChatBack) btnChatBack.addEventListener('click', () => {
  state.currentSessionId = null;
  showPage('consult');
});

$('#btn-back-from-settings').addEventListener('click', () => {
  // FE-7: navigate back to where user came from
  const from = state._settingsFrom;
  state._settingsFrom = null;
  if (from === 'home') { switchTab('home'); }
  else if (from === 'context') { switchTab('context'); }
  else if (from === 'time') { switchTab('time'); }
  else if (state.currentSessionId) { showPage('consult'); }
  else { showPage('consult'); }
});

document.addEventListener('click', function(e) {
  if (!e.target.closest('#context-menu')) {
    closeContextMenu();
  }
});

document.addEventListener('scroll', function() {
  closeContextMenu();
});

  function sendFeedback(sessionId, msgId, rating) {
    const container = document.querySelector('.feedback-btns[data-msg-id="' + CSS.escape(msgId) + '"]');
    if (!container) return;

    if (rating === 'inaccurate') {
      // Avoid double-injecting the correction area
      if (container.querySelector('.fb-correction')) return;
      const s = getSession(sessionId);
      const turnIndex = s ? s.messages.findIndex(function(m) { return m.id === msgId; }) : -1;

      // DOM-built UI: avoids insertAdjacentHTML XSS risk.
      const corrDiv = document.createElement('div');
      corrDiv.className = 'fb-correction';
      corrDiv.style.cssText = 'margin-top:6px;display:flex;flex-direction:column;gap:4px';

      const ta = document.createElement('textarea');
      ta.placeholder = 'What was wrong? (optional)';
      ta.rows = 2;
      ta.style.cssText = 'width:100%;padding:6px 8px;border-radius:var(--radius-xs);border:1px solid var(--border);' +
        'background:var(--bg-input);color:var(--text);font-size:var(--fs-sm);font-family:var(--font);resize:none;outline:none';
      corrDiv.appendChild(ta);

      const btnRow = document.createElement('div');
      btnRow.style.cssText = 'display:flex;gap:6px';

      const submitBtn = document.createElement('button');
      submitBtn.className = 'btn btn-primary';
      submitBtn.style.cssText = 'font-size:var(--fs-sm);padding:4px 12px';
      submitBtn.textContent = 'Submit';
      submitBtn.addEventListener('click', function() { submitCorrection(sessionId, msgId, turnIndex); });

      const cancelBtn = document.createElement('button');
      cancelBtn.className = 'btn btn-cancel';
      cancelBtn.style.cssText = 'font-size:var(--fs-sm);padding:4px 12px';
      cancelBtn.textContent = 'Cancel';
      cancelBtn.addEventListener('click', function() { corrDiv.remove(); });

      btnRow.appendChild(submitBtn);
      btnRow.appendChild(cancelBtn);
      corrDiv.appendChild(btnRow);
      container.appendChild(corrDiv);
      ta.focus();
      return;
    }

    const s = getSession(sessionId);
    const turnIndex = s ? s.messages.findIndex(function(m) { return m.id === msgId; }) : -1;
    fetch('/api/feedback', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session_id: sessionId, turn_index: turnIndex, rating: rating })
    }).then(function(r) { return r.json(); }).then(function(data) {
      if (data.ok) {
        const btns = document.querySelectorAll('.fb-btn[data-msg-id="' + CSS.escape(msgId) + '"]');
        btns.forEach(function(b) { b.classList.add('done'); b.textContent = '✓'; });
        showToast('Feedback sent');
      }
    }).catch(function() { showToast('Failed to send feedback', 'warn'); });
  }

  function submitCorrection(sessionId, msgId, turnIndex) {
    const container = document.querySelector('.feedback-btns[data-msg-id="' + CSS.escape(msgId) + '"]');
    if (!container) return;
    const ta = container.querySelector('.fb-correction textarea');
    const correction = ta ? ta.value.trim() : '';
    fetch('/api/feedback', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session_id: sessionId, turn_index: turnIndex, rating: 'inaccurate', correction: correction })
    }).then(r => r.json()).then(data => {
      if (data.ok) {
        try {
          const btns = document.querySelectorAll('.fb-btn[data-msg-id="' + CSS.escape(msgId) + '"]');
          btns.forEach(b => { b.classList.add('done'); b.textContent = '✓'; });
          const fc = container.querySelector('.fb-correction');
          if (fc) fc.remove();
        } catch (_) {}
        showToast('反馈已发送');
      }
    }).catch(() => showToast('发送失败', 'warn'));
  }

  function memAction(action, id) {
    let url = '/api/memory/' + encodeURIComponent(id);
    let method = 'PUT';
    if (action === 'delete') { method = 'DELETE'; }
    else { url += '/' + action; }
    fetch(url, { method: method })
      .then(r => r.json())
      .then(d => {
        if (d && (d.ok || method === 'DELETE')) {
          showToast(action === 'confirm' ? '已确认' : action === 'dispute' ? '已标记不准确' : '已删除');
          loadContextMemories();
        } else {
          showToast('操作失败', 'warn');
        }
      })
      .catch(() => showToast('操作失败', 'warn'));
  }
