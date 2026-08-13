let _calFilterDate = '';

function loadTasks() {
  const container = $('#task-list');
  container.innerHTML = '<div class="empty-state">···</div>';
  fetch('/api/tasks')
    .then(r => r.json())
    .then(resp => {
      const tasks = (resp && resp.tasks) || [];
      renderTasks(tasks);
    })
    .catch(() => { container.innerHTML = '<div class="empty-state">Failed to load.</div>'; });
}

// Parse as LOCAL midnight: new Date("YYYY-MM-DD") is UTC midnight, which flips the day in UTC+8.
function parseLocalDate(s) {
  const p = s.slice(0, 10).split('-');
  return new Date(Number(p[0]), Number(p[1]) - 1, Number(p[2]));
}

function formatDueDate(dueDate) {
  if (!dueDate) return '';
  // Compare calendar dates, not instants — diffing against "now" marked today's tasks "Overdue 1d".
  const d = parseLocalDate(dueDate);
  const now = new Date();
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const diffDays = Math.round((d - today) / 86400000);
  if (diffDays < 0) return '<span style="color:var(--danger)">Overdue ' + Math.abs(diffDays) + 'd</span>';
  if (diffDays === 0) return '<span style="color:var(--warning)">Today</span>';
  if (diffDays === 1) return '<span style="color:var(--warning)">Tomorrow</span>';
  if (diffDays <= 7) return 'In ' + diffDays + ' days';
  return d.toLocaleDateString('en-US', {month:'short', day:'numeric'});
}

function renderTasks(tasks) {
  const container = $('#task-list');
  let html = '';
  const filterDate = _calFilterDate;
  let filteredTasks = tasks;
  if (filterDate) {
    filteredTasks = tasks.filter(t => t.end_date && t.end_date.slice(0, 10) === filterDate);
    const d = new Date(filterDate + 'T00:00');
    const label = d.toLocaleDateString('en-US', { weekday: 'short', month: 'short', day: 'numeric' });
    html += '<div class="task-section-title" style="display:flex;align-items:center;justify-content:space-between">' +
      '<span>' + escHtml(label) + '</span>' +
      '<button class="btn-small" onclick="clearCalFilter()" style="font-size:10px;padding:2px 6px">All</button>' +
    '</div>';
  } else {
    html += '<div class="task-section-title">Tasks</div>';
  }
  if (filteredTasks.length) {
    filteredTasks.forEach(t => {
      const isOverdue = t.end_date && !t.done &&
        parseLocalDate(t.end_date) < new Date(new Date().setHours(0, 0, 0, 0));
      const hasDetail = !!(t.description && t.description.length > 0);
      html += '<div class="task-row' + (t.done ? ' done' : '') + (isOverdue ? ' overdue' : '') + '" data-id="' + escHtml(t.id) + '">' +
        '<div class="swipe-bg"><span>Delete</span></div>' +
        '<div class="task-check" data-role="toggle">' + (t.done ? '&#10003;' : '') + '</div>' +
        '<div class="task-main" data-role="expand">' +
          '<div class="task-title">' + escHtml(t.title || '') + '</div>' +
          (hasDetail ? '<div class="task-desc" style="font-size:var(--fs-xs);color:var(--text-hint);white-space:nowrap;overflow:hidden;text-overflow:ellipsis">' + escHtml(t.description) + '</div>' : '') +
          (t.end_date ? '<div class="task-due">' + formatDueDate(t.end_date) + '</div>' : '') +
        '</div>' +
        '<button class="btn-small" data-role="edit" style="flex-shrink:0;font-size:11px;margin-left:4px">✎</button>' +
      '</div>';
    });
  } else if (filterDate) {
    html += '<div class="alert-item"><span style="color:var(--text-hint)">No tasks for this day</span></div>';
  } else {
    html += '<div class="alert-item"><span style="color:var(--text-hint)">No tasks</span></div>';
  }

  container.innerHTML = html;
  initTaskTouch(tasks);
}

function initTaskTouch(tasks) {
  const byId = {};
  tasks.forEach(t => { byId[t.id] = t; });
  $('#task-list').querySelectorAll('.task-row').forEach(el => {
    let startX = 0, startY = 0, swiping = false;
    el.addEventListener('touchstart', e => {
      startX = e.touches[0].clientX; startY = e.touches[0].clientY; swiping = false;
    }, { passive: true });
    el.addEventListener('touchmove', e => {
      const dx = e.touches[0].clientX - startX;
      const dy = e.touches[0].clientY - startY;
      if (Math.abs(dx) > Math.abs(dy) && Math.abs(dx) > 5) {
        swiping = true;
        if (dx < -30) el.classList.add('swiped');
        else if (dx > 10) el.classList.remove('swiped');
      }
    }, { passive: true });
    el.addEventListener('touchend', () => {
      if (el.classList.contains('swiped')) {
        clearTimeout(el._swipeTimer);
        el._swipeTimer = setTimeout(() => el.classList.remove('swiped'), 1200);
      }
    });
    el.addEventListener('click', e => {
      const id = el.dataset.id;
      if (el.classList.contains('swiped')) {
        if (e.target.closest('.swipe-bg')) {
          deleteTask(id);
        }
        el.classList.remove('swiped');
        clearTimeout(el._swipeTimer);
        return;
      }
      const t = byId[id];
      if (!t) return;
      if (e.target.closest('[data-role="toggle"]')) {
        toggleTask(t);
      } else if (e.target.closest('[data-role="edit"]')) {
        editTask(t);
      } else if (e.target.closest('[data-role="expand"]')) {
        // toggle full details (file-move semantics: summary+details live in Task).
        expandTaskDetail(t);
      }
    });
  });
}

function toggleTask(t) {
  fetch('/api/tasks/' + encodeURIComponent(t.id), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title: t.title, end_date: t.end_date || '', done: !t.done })
  }).then(r => r.json()).then(d => {
    if (d.ok) loadTasks();
    else showToast('Update failed', 'warn');
  }).catch(() => showToast('Update failed', 'warn'));
}

// toggle description: collapsed to one line, expanded shows full text with original formatting.
function expandTaskDetail(t) {
  const row = Array.from(document.querySelectorAll('.task-row')).find(el => el.dataset.id === t.id);
  if (!row) return;
  const main = row.querySelector('[data-role="expand"]');
  if (!main) return;
  const desc = main.querySelector('.task-desc');
  if (!desc) return;
  const expanded = main.classList.toggle('expanded');
  if (expanded) {
    desc.style.whiteSpace = 'pre-wrap';
    desc.style.overflow = 'visible';
    desc.style.textOverflow = 'clip';
    desc.style.maxHeight = 'none';
  } else {
    desc.style.whiteSpace = 'nowrap';
    desc.style.overflow = 'hidden';
    desc.style.textOverflow = 'ellipsis';
    desc.style.maxHeight = '';
  }
}

function editTask(t) {
  var row = Array.from(document.querySelectorAll('.task-row')).find(el => el.dataset.id === t.id);
  if (!row) return;
  var main = row.querySelector('[data-role="expand"]');
  if (!main) return;
  var origTitle = t.title || '', origDue = t.end_date || '';
  main.innerHTML =
    '<input class="et-title" value="' + escHtml(origTitle) + '" style="width:100%;padding:4px 6px;background:var(--bg-input);color:var(--text);border:1px solid var(--border);border-radius:var(--radius-xs);font-family:var(--font);font-size:var(--fs-md)" autofocus>' +
    '<input class="et-due" type="date" value="' + escHtml(origDue.slice(0, 10)) + '" style="width:100%;margin-top:4px;padding:4px 6px;background:var(--bg-input);color:var(--text-dim);border:1px solid var(--border);border-radius:var(--radius-xs);font-family:var(--font);font-size:var(--fs-sm)">' +
    '<div style="display:flex;gap:4px;margin-top:4px">' +
      '<button class="btn-small et-save">Save</button>' +
      '<button class="btn-small danger et-cancel">Cancel</button>' +
    '</div>';
  main.querySelector('.et-save').addEventListener('click', function() {
    var n = main.querySelector('.et-title').value.trim();
    if (!n) return;
    var d = main.querySelector('.et-due').value;
    fetch('/api/tasks/' + encodeURIComponent(t.id), {
      method: 'PUT', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({title: n, end_date: d, done: !!t.done})
    }).then(function(r) { return r.json(); }).then(function(d) {
      if (d.ok) loadTasks(); else showToast('Save failed', 'warn');
    }).catch(function() { showToast('Save failed', 'warn'); });
  });
  main.querySelector('.et-cancel').addEventListener('click', function() { loadTasks(); });
}

function deleteTask(id) {
  fetch('/api/tasks/' + encodeURIComponent(id), { method: 'DELETE' })
    .then(r => r.json()).then(d => {
      if (d.ok) { loadTasks(); showToast('Deleted'); }
      else showToast('Delete failed', 'warn');
    }).catch(() => showToast('Delete failed', 'warn'));
}

function addTask() {
  const titleEl = $('#task-add-title');
  const dueEl = $('#task-add-end');
  const title = (titleEl.value || '').trim();
  if (!title) { showToast('Task title required', 'warn'); return; }
  const body = { title: title };
  if (dueEl.value) body.end_date = dueEl.value;
  fetch('/api/tasks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  }).then(function(r) {
    return r.json().then(function(d) { return { ok: r.ok, d: d }; });
  }).then(function(res) {
    if (!res.ok) {
      showToast((res.d && res.d.error) || 'Add failed', 'warn');
      return;
    }
    titleEl.value = ''; dueEl.value = '';
    _calFilterDate = ''; // clear any date filter on new add
    loadTasks();
    showToast('Task added');
  }).catch(() => showToast('Add failed', 'warn'));
}

// runStar streams /api/star (SSE) and returns a prioritized plan; no user input needed (unlike Consult).
let _starReader = null;
let _starCancelled = false; // closeStar mid-stream: finish() must not run
let _starBusy = false;      // accept/save in flight: block double-submit
let lastStarResult = ''; // latest Star result, used to drop it from history when converted to a Task

// Strict YYYY-MM-DD: daemon 400s and LLM suggestions carry garbage like "2026-13-45" — degrade to today.
function sanitizeStarDate(s) {
  return /^\d{4}-\d{2}-\d{2}$/.test(s || '') ? s : '';
}

// Clamp to allowed set — "urgent" would misrender the card and the <select> would silently default wrongly.
function normalizeStarPriority(p) {
  return (p === 'high' || p === 'low') ? p : 'normal';
}

function runStar() {
  const btn = $('#btn-star');
  const panel = $('#star-result');
  if (btn.disabled) return;
  recordUsage('do');
  // Fresh start: a leftover suggestion would suppress the fallback card when suggest-task fails.
  _starSuggestion = null;
  _starCancelled = false;
  btn.disabled = true;
  btn.textContent = '✦';
  panel.style.display = '';
  panel.innerHTML = '<div class="star-body"><span class="star-cursor">▍</span></div>';
  const bodyEl = panel.querySelector('.star-body');

  // Local device time so advice reflects the user's actual clock.
  const d = new Date();
  const pad = n => (n < 10 ? '0' + n : '' + n);
  const week = ['Sun','Mon','Tue','Wed','Thu','Fri','Sat'][d.getDay()];
  const now = d.getFullYear() + '-' + pad(d.getMonth()+1) + '-' + pad(d.getDate()) +
              ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes()) + ' ' + week;

    let acc = '';
    // If the user closed mid-stream, finish() must not persist a half result into the hidden panel.
    fetch('/api/star', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ now: now })
  }).then(r => {
    if (!r.ok) { return r.text().then(t => { throw new Error(t || ('HTTP ' + r.status)); }); }
    const reader = r.body.getReader();
    _starReader = reader;
    const decoder = new TextDecoder();
    let buf = '';
    let errored = false;
    let doneSent = false;
    function finish() {
      _starReader = null;
      if (_starCancelled) return; // user closed mid-stream: drop everything
      btn.disabled = false;     btn.textContent = '✦';
      if (!errored && !acc.trim()) {
        bodyEl.innerHTML = '<span class="empty-state">No tasks yet.</span>';
      } else if (!errored && acc.trim()) {
        lastStarResult = acc.trim();
        try {
          const doHistory = JSON.parse(localStorage.getItem('oy_star_history_v1') || '[]');
          doHistory.unshift({ time: Date.now(), result: acc.trim() });
          if (doHistory.length > 30) doHistory.length = 30;
          localStorage.setItem('oy_star_history_v1', JSON.stringify(doHistory));
        } catch(e) {}
        // No suggest-task event → derive the card from the Star result (accepting must always yield a task).
        if (!_starSuggestion || !_starSuggestion.title) {
          const t = deriveTaskTitle();
          if (t) handleStarSuggestion({ title: t, description: '', end_date: '', priority: 'normal' });
        }
        const addBtn = document.createElement('div');
        addBtn.style.cssText = 'margin-top:12px;padding-top:8px;border-top:1px solid var(--border);display:flex;gap:6px';
        addBtn.innerHTML = '<button class="btn btn-cancel" style="font-size:var(--fs-sm)" onclick="closeStar()">关闭</button>';
        panel.appendChild(addBtn);
        panel.scrollTop = panel.scrollHeight;
      }
    }
    function pump() {
      return reader.read().then(({done, value}) => {
        if (done) { if (!doneSent) finish(); return; }
        buf += decoder.decode(value, {stream: true});
        const lines = buf.split('\n');
        buf = lines.pop();
        let pendingEvent = null;
        for (const line of lines) {
          if (line.startsWith('event: ')) { pendingEvent = line.slice(7).trim(); continue; }
          if (!line.startsWith('data: ')) { if (!line.trim()) pendingEvent = null; continue; }
          const payload = line.slice(6);
          if (pendingEvent === 'suggest-task') {
            try { handleStarSuggestion(JSON.parse(payload)); } catch(e) {}
            pendingEvent = null;
            continue;
          }
          pendingEvent = null;
          if (payload === '[DONE]') {
            // [DONE] only finalizes the button state; keep reading for the suggest-task event.
            doneSent = true;
            finish();
            continue;
          }
          try {
            const ev = JSON.parse(payload);
            if (ev.type === 'text-delta' && ev.text) {
              acc += ev.text;
              bodyEl.innerHTML = formatContent(acc);
              panel.scrollTop = panel.scrollHeight;
            } else if (ev.type === 'error') {
              errored = true;
              bodyEl.innerHTML = '<span style="color:var(--danger)">Error: ' + escHtml(ev.error || '') + '</span>';
              btn.disabled = false;     btn.textContent = '✦';
              reader.cancel();
              _starReader = null;
              return;
            }
          } catch (e) { /* ignore keepalive / non-JSON */ }
        }
        return pump();
      });
    }
    return pump();
  }).catch(e => {
    _starReader = null;
    btn.disabled = false;     btn.textContent = '✦';
    bodyEl.innerHTML = '<span style="color:var(--danger)">Error: ' + escHtml(e.message || '') + '</span>';
  });
}

let _starSuggestion = null;

// Accepting a Star result must ALWAYS yield a task, even when the daemon's suggest-task call failed.
function deriveTaskTitle() {
  const lines = (lastStarResult || '').split('\n');
  for (let i = 0; i < lines.length; i++) {
    const t = lines[i].trim()
      .replace(/^(#+|[-*>]|\d+[.)])\s*/, '')
      .replace(/\*\*/g, '')
      .trim();
    if (t.length >= 5) return t.slice(0, 60);
  }
  return '';
}

function handleStarSuggestion(s) {
  const panel = $('#star-result');
  if (!panel || !s || !s.title) return;
  // Assign AFTER dismissStarSuggestion() — it nulls _starSuggestion.
  dismissStarSuggestion();
  _starSuggestion = s;
  const wrap = document.createElement('div');
  wrap.className = 'star-suggest-card';
  wrap.style.cssText = 'margin-top:12px;padding:10px;border:1px solid var(--border);border-radius:10px;background:var(--bg-card, #161b22)';
  wrap.innerHTML =
    '<div style="font-size:var(--fs-sm);color:var(--text-hint);margin-bottom:6px">📋 Star 建议任务（已精简）</div>' +
    '<div style="font-weight:600;margin-bottom:4px">' + escHtml(s.title) + '</div>' +
    (s.description ? '<div style="font-size:var(--fs-sm);color:var(--text-dim);margin-bottom:6px">' + escHtml(s.description) + '</div>' : '') +
    '<div style="display:flex;gap:8px;font-size:var(--fs-xs);color:var(--text-hint);margin-bottom:8px;flex-wrap:wrap">' +
      '<span>🗓 ' + escHtml(s.end_date || '今天') + '</span>' +
      '<span>' + escHtml((s.priority || 'normal') === 'high' ? '🔴 高' : (s.priority || 'normal') === 'low' ? '🟢 低' : '🟡 中') + '</span>' +
    '</div>' +
    '<div style="display:flex;gap:6px">' +
      '<button class="btn btn-primary" style="font-size:var(--fs-sm);flex:1" onclick="acceptStarSuggestion()">接受</button>' +
      '<button class="btn btn-cancel" style="font-size:var(--fs-sm);flex:1" onclick="editStarSuggestion()">编辑</button>' +
      '<button class="btn btn-cancel" style="font-size:var(--fs-sm)" onclick="dismissStarSuggestion()">×</button>' +
    '</div>';
  panel.appendChild(wrap);
  panel.scrollTop = panel.scrollHeight;
}

function acceptStarSuggestion() {
  if (_starBusy) return; // block double-submit creating duplicate tasks
  // Accept always creates a task: fall back to deriving the title from the Star result itself.
  let s = _starSuggestion;
  if (!s || !s.title) {
    const t = deriveTaskTitle();
    if (!t) { showToast('No Star result', 'warn'); return; }
    s = { title: t, description: '', end_date: '', priority: 'normal' };
  }
  // Star→Task conversion: long text → Task.description, summary → title; delete the Star record on accept.
  const fullText = lastStarResult || s.description || '';
  _starBusy = true;
  fetch('/api/tasks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      title: s.title,
      description: fullText,
      end_date: sanitizeStarDate(s.end_date),
      priority: normalizeStarPriority(s.priority)
    })
  }).then(function(r) {
    // r.json() resolves on 400 too — ok distinguishes success; treating 400 as success deleted Star history.
    return r.json().then(function(d) { return { ok: r.ok, d: d }; });
  }).then(function(res) {
    _starBusy = false;
    if (!res.ok) {
      showToast((res.d && res.d.error) || 'Task add failed', 'warn');
      return; // keep the Star result so the user can retry
    }
    removeCurrentStarFromHistory();
    dismissStarSuggestion();
    closeStar();
    showPage('time');
    loadTasks();
    showToast('Task added');
  }, function() {
    _starBusy = false;
    showToast('Add failed', 'warn');
  });
}

function editStarSuggestion() {
  let s = _starSuggestion;
  if (!s || !s.title) {
    // Same fallback as accept: editing must work without a structured suggestion.
    const t = deriveTaskTitle();
    if (!t) return;
    s = { title: t, description: '', end_date: '', priority: 'normal' };
    _starSuggestion = s;
  }
  const panel = $('#star-result');
  if (!panel) return;
  const card = panel.querySelector('.star-suggest-edit');
  const html =
    '<div class="star-suggest-edit" style="margin-top:10px;padding:10px;border:1px solid var(--border);border-radius:10px">' +
    '<input type="text" id="sug-title" value="' + escHtml(s.title || '') + '" placeholder="任务标题" style="width:100%;margin-bottom:6px">' +
    '<input type="text" id="sug-desc" value="' + escHtml(s.description || '') + '" placeholder="简要行动项描述" style="width:100%;margin-bottom:6px">' +
    '<div style="display:flex;gap:6px;margin-bottom:6px">' +
      '<input type="date" id="sug-date" value="' + escHtml((s.end_date || '').slice(0, 10)) + '" style="flex:1">' +
      '<select id="sug-priority" style="flex:1">' +
        '<option value="high"' + (s.priority === 'high' ? ' selected' : '') + '>🔴 高</option>' +
        '<option value="normal"' + (!s.priority || s.priority === 'normal' ? ' selected' : '') + '>🟡 中</option>' +
        '<option value="low"' + (s.priority === 'low' ? ' selected' : '') + '>🟢 低</option>' +
      '</select>' +
    '</div>' +
    '<div style="display:flex;gap:6px">' +
      '<button class="btn btn-primary" style="font-size:var(--fs-sm);flex:1" onclick="saveEditedStarSuggestion()">保存</button>' +
      '<button class="btn btn-cancel" style="font-size:var(--fs-sm)" onclick="dismissStarSuggestion()">取消</button>' +
    '</div>' +
    '</div>';
  if (card) { card.outerHTML = html; } else { panel.appendChild(createElFromHTML(html)); }
  panel.scrollTop = panel.scrollHeight;
}

function saveEditedStarSuggestion() {
  if (_starBusy) return;
  const title = ($('#sug-title') || {}).value || '';
  const desc = ($('#sug-desc') || {}).value || '';
  const date = ($('#sug-date') || {}).value || '';
  const priority = ($('#sug-priority') || {}).value || 'normal';
  if (!title.trim()) { showToast('Title required', 'warn'); return; }
  // when no description is given, fall back to the full Star text (file-move semantics).
  const fullText = desc.trim() || lastStarResult || '';
  _starBusy = true;
  fetch('/api/tasks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      title: title.trim(),
      description: fullText,
      end_date: sanitizeStarDate(date),
      priority: normalizeStarPriority(priority)
    })
  }).then(function(r) {
    return r.json().then(function(d) { return { ok: r.ok, d: d }; });
  }).then(function(res) {
    _starBusy = false;
    if (!res.ok) {
      showToast((res.d && res.d.error) || 'Task add failed', 'warn');
      return;
    }
    removeCurrentStarFromHistory();
    dismissStarSuggestion();
    closeStar();
    showPage('time');
    loadTasks();
    showToast('Task added');
  }, function() {
    _starBusy = false;
    showToast('Add failed', 'warn');
  });
}

function dismissStarSuggestion() {
  _starSuggestion = null;
  const panel = $('#star-result');
  if (panel) {
    panel.querySelectorAll('.star-suggest-edit, .star-suggest-card').forEach(el => el.remove());
  }
}

// Star→Task conversion: on accept remove the entry from Star history by lastStarResult (it moved, not kept).
function removeCurrentStarFromHistory() {
  if (!lastStarResult) return;
  try {
    const doHistory = JSON.parse(localStorage.getItem('oy_star_history_v1') || '[]');
    const kept = doHistory.filter(function(h) { return h.result !== lastStarResult; });
    if (kept.length !== doHistory.length) {
      localStorage.setItem('oy_star_history_v1', JSON.stringify(kept));
    }
  } catch(e) {}
  lastStarResult = '';
}

function createElFromHTML(html) {
  const tpl = document.createElement('template');
  tpl.innerHTML = html.trim();
  return tpl.content.firstChild;
}

function closeStar() {
  const btn = $('#btn-star');
  _starCancelled = true; // tell a mid-flight finish() to stand down
  _starSuggestion = null;
  if (_starReader) { _starReader.cancel(); _starReader = null; }
  if (btn) {
    btn.disabled = true; // briefly disabled while cancelling
    setTimeout(() => {
      btn.disabled = false;
      btn.textContent = '✦';
    }, 200);
  }
  const panel = $('#star-result');
  if (panel) { panel.style.display = 'none'; panel.innerHTML = ''; }
}

function addStarSuggestionAsTask() {
  // Legacy safety fallback: capture the first line of the Do result when no suggest-task event arrived.
  const panel = $('#star-result');
  const lines = ((panel && panel.querySelector('.star-body')) || {}).innerText || '';
  const firstLine = lines.split('\n').find(l => l.trim().length > 5) || 'From Do suggestion';
  const title = firstLine.replace(/^[\d\.\-\*\s]+/, '').slice(0, 60);
  fetch('/api/tasks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title: title })
  }).then(r => r.json()).then(() => {
    closeStar();
    showPage('time');
    loadTasks();
    showToast('Task added from Do');
  }).catch(() => showToast('Add failed', 'warn'));
}

function calDayClick(dateStr) {
  _calFilterDate = dateStr;
  loadTimeSub('task');
  setTimeout(() => {
    const d = new Date(dateStr + 'T00:00');
    const label = d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
    showToast('Tasks for ' + label);
  }, 300);
}

function clearCalFilter() {
  _calFilterDate = '';
  loadTasks();
}
