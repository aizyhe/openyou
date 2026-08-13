'use strict';

const STORAGE_KEY = 'oy_settings_v1';
const SESSIONS_KEY = 'oy_sessions_v1';
const BUILD_DATE = '2026-08-10';
const APP_VERSION = '1.0.1';

// local-only stats, never uploaded.
const STATS_KEY = 'oy_usage_stats_v1';
function recordUsage(event) {
  try {
    const stats = JSON.parse(localStorage.getItem(STATS_KEY) || '{}');
    // toISOString() is UTC; use the local date so a UTC+8 pre-midnight use isn't backdated a day.
    const now = new Date();
    const today = now.getFullYear() + '-' +
      String(now.getMonth() + 1).padStart(2, '0') + '-' +
      String(now.getDate()).padStart(2, '0');
    if (!stats[today]) stats[today] = {};
    stats[today][event] = (stats[today][event] || 0) + 1;
    const keys = Object.keys(stats).sort().slice(-30);
    const trimmed = {};
    keys.forEach(k => { trimmed[k] = stats[k]; });
    localStorage.setItem(STATS_KEY, JSON.stringify(trimmed));
  } catch(e) {}
}

let state = {
  settings: {},
  sessions: [],
  currentSessionId: null,
  ws: null,
  wsReconnectDelay: 1000,
  wsReconnectTimer: null,
  isConnected: false,
  isStreaming: false,
  streamingMsgIdx: -1,
  streamingSessionId: null,
  currentStreamingText: '',
  pendingSendQueue: [],
  pressTimer: null,
  longPressTarget: null,
  currentPage: 'consult',
  thinkTankActive: true,
  thinkTankSkills: [],
  lastUserMessage: '',
  memFilterType: '',
  _contextPersonName: null,
  llmSendTime: 0,
  _suppressClick: false,
  llmLatencies: [],
  daemonOnline: false,
  daemonHasKey: false,
  settingsDirty: false,
  contextData: null,
};

const $ = (sel, ctx) => (ctx || document).querySelector(sel);
const $$ = (sel, ctx) => [...(ctx || document).querySelectorAll(sel)];

const pages = {
  consult: $('#consult-page'),
  settings: $('#settings-page'),
  skills: $('#skills-page'),
  context: $('#context-page'),
  time: $('#time-page'),
  home: $('#home-page'),
};

function showToast(msg, type) {
  const el = $('#toast');
  el.textContent = msg;
  el.className = 'toast show' + (type ? ' ' + type : '');
  clearTimeout(el._timer);
  el._timer = setTimeout(() => { el.classList.remove('show'); }, 2500);
}

function ts() { return new Date().toISOString(); }

function genId() { return crypto.randomUUID ? crypto.randomUUID() : 's' + Date.now() + '-' + Math.random().toString(36).slice(2, 10); }

function debounce(fn, ms) { var t; return function() { var args = arguments, self = this; clearTimeout(t); t = setTimeout(function() { fn.apply(self, args); }, ms); }; }

function formatTime(val) {
  if (!val) return '';
  let d;
  if (typeof val === 'number') {
    // Go int64 ns timestamps exceed JS safe integers; gate on the 1e15 threshold.
    if (val > 1e15) val = Math.floor(val / 1e6);
    else if (val < 1e10) val = val * 1000;
  }
  d = new Date(val);
  if (isNaN(d.getTime())) return '';
  const now = new Date();
  const diff = now - d;
  if (diff < 60000) return 'Just now';
  if (diff < 3600000) return Math.floor(diff / 60000) + 'm ago';
  if (diff < 86400000) return Math.floor(diff / 3600000) + 'h ago';
  const pad = n => String(n).padStart(2, '0');
  return pad(d.getMonth() + 1) + '/' + pad(d.getDate()) + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
}

function defaultSettings() {
  return {
    provider: 'deepseek',
    model: 'deepseek-v4-pro',
    apiKey: '',
    baseUrl: 'https://api.deepseek.com/v1',
    maxTokens: 4096,
    temperature: 0.7,
    privacyMode: 'balanced',
    appLockEnabled: false,
    appLockTimeout: 5,
    doNotDisturb: false,
    doNotDisturbStart: '22:00',
    doNotDisturbEnd: '08:00',
    backupTime: null,
    onboardingDone: false,
  };
}

function loadSettings() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    state.settings = raw ? { ...defaultSettings(), ...JSON.parse(raw) } : defaultSettings();
  } catch (e) {
    state.settings = defaultSettings();
  }
}

function saveSettings() {
  try { localStorage.setItem(STORAGE_KEY, JSON.stringify(state.settings)); } catch (e) {
    console.warn('saveSettings: localStorage quota exceeded, settings not saved', e);
    if (!saveSettings._warned) {
      saveSettings._warned = true;
      showToast && showToast('Storage full, settings may not save');
    }
  }
  // Send api_key only when typed — an empty value would wipe a key from Settings or userdata/openyou.json.
  const payload = {
    base_url: state.settings.baseUrl,
    model: state.settings.model,
    max_tokens: state.settings.maxTokens,
    temperature: state.settings.temperature,
    privacy_mode: state.settings.privacyMode
  };
  if (state.settings.apiKey) payload.api_key = state.settings.apiKey;
  fetch('/api/config', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
  }).catch(e => { /* daemon not up yet */ });
}

// daemon is the source of truth for api_key/model/base_url.
function syncDaemonConfig() {
  fetch('/api/config')
    .then(r => r.json())
    .then(cfg => {
      let changed = false;
      // api_key is masked; merging would overwrite the real key — has_key drives the read-only field.
      if (cfg.base_url && state.settings.baseUrl !== cfg.base_url) {
        state.settings.baseUrl = cfg.base_url;
        changed = true;
      }
      if (cfg.model && state.settings.model !== cfg.model) {
        state.settings.model = cfg.model;
        changed = true;
      }
      if (cfg.max_tokens !== undefined && cfg.max_tokens > 0 && state.settings.maxTokens !== cfg.max_tokens) {
        state.settings.maxTokens = cfg.max_tokens;
        changed = true;
      }
      if (cfg.temperature !== undefined && state.settings.temperature !== cfg.temperature) {
        state.settings.temperature = cfg.temperature;
        changed = true;
      }
      if (changed) {
        try { localStorage.setItem(STORAGE_KEY, JSON.stringify(state.settings)); } catch (e) {}
      }
      state.daemonHasKey = cfg.has_key === 'true' || cfg.has_key === true;
    })
    .catch(() => {}); // daemon not ready yet — will retry on next WS connect
}

function loadSessions() {
  try {
    const raw = localStorage.getItem(SESSIONS_KEY);
    const parsed = raw ? JSON.parse(raw) : [];
    // tolerate legacy snake_case fields from older storage/server sync.
    state.sessions = parsed.map(s => {
      if (s.updated_at !== undefined && s.updatedAt === undefined) s.updatedAt = s.updated_at;
      if (s.created_at !== undefined && s.createdAt === undefined) s.createdAt = s.created_at;
      return s;
    });
  } catch (e) {
    state.sessions = [];
  }
}

function _sessionsMsgCount(sessions) {
  return sessions.reduce((n, s) => n + (s.messages ? s.messages.length : 0), 0);
}

function saveSessions() {
  try {
    let sessions = state.sessions;
    const totalMsgs = _sessionsMsgCount(sessions);
    if (totalMsgs > 500 || sessions.length > 30) {
      let kept = sessions.slice(0, 30);
      while (kept.length > 1 && _sessionsMsgCount(kept) > 500) {
        kept = kept.slice(0, kept.length - 1);
      }
      if (kept.length < sessions.length) {
        state.sessions = kept;
        sessions = kept;
        showToast && showToast('Oldest consults cleared to free storage', 'warn');
      }
    }
    localStorage.setItem(SESSIONS_KEY, JSON.stringify(sessions));
  } catch (e) {
    console.warn('saveSessions: localStorage quota exceeded, sessions not saved', e);
    if (!saveSessions._warned) {
      saveSessions._warned = true;
      showToast && showToast('Storage full, consults may not save', 'warn');
    }
  }
}

function createSession() {
  const s = {
    id: genId(),
    title: 'New Consult',
    messages: [],
    createdAt: ts(),
    updatedAt: ts(),
  };
  state.sessions.unshift(s);
  saveSessions();
  return s;
}

function getSession(id) {
  return state.sessions.find(s => s.id === id);
}

function deleteSession(id) {
  const s = getSession(id);
  if (!s) return;
  state.sessions = state.sessions.filter(x => x.id !== id);
  if (state.currentSessionId === id) { state.currentSessionId = null; showPage('home'); }
  renderSessionList();
  saveSessions();
  fetch('/api/sessions/' + encodeURIComponent(id), { method: 'DELETE' })
    .catch(() => { showToast('Delete failed', 'warn'); });
  showToast('Consult deleted');
}

function updateSessionTitle(id, title) {
  const s = getSession(id);
  if (s) { s.title = title; s.updatedAt = ts(); saveSessions(); }
}

function addMessage(sessionId, role, content, extra) {
  const s = getSession(sessionId);
  if (!s) return null;
  const msg = {
    id: genId(),
    role: role,
    content: content,
    time: ts(),
    tools: extra && extra.tools ? extra.tools : [],
    citations: extra && extra.citations ? extra.citations : [],
    interrupted: !!(extra && extra.interrupted),
  };
  s.messages.push(msg);
  s.updatedAt = ts();
  if (role === 'user' && s.title === 'New Consult' && s.messages.filter(m => m.role === 'user').length === 1) {
    s.title = content.slice(0, 40);
  }
  saveSessions();
  return msg;
}

function updateLastAssistantMsg(sessionId, deltas, tools, citations) {
  const s = getSession(sessionId);
  if (!s) return;
  let last = null;
  for (let i = s.messages.length - 1; i >= 0; i--) {
    if (s.messages[i].role === 'assistant') { last = s.messages[i]; break; }
  }
  if (!last) return;
  if (deltas !== undefined) last.content = (last.content || '') + deltas;
  if (tools !== undefined) last.tools = tools;
  if (citations !== undefined) last.citations = citations;
  s.updatedAt = ts();
  saveSessions();
  return last;
}

function showPage(name, sub) {
  Object.values(pages).forEach(p => p.classList.remove('active'));
  pages[name].classList.add('active');
  state.currentPage = name;
  if (name === 'consult') renderConsultPage(sub);
  if (name === 'settings') renderSettings();
  if (name === 'skills') renderSkillsPage();
  if (name === 'context') renderContextPage(sub);
  if (name === 'time') renderTimePage(sub);
  if (name === 'home') loadHome();
}

function renderConsultPage(sub) {
  bindConsultSubtabs();
  const saved = consultSubLocal();
  loadConsultSub(sub || (saved && (saved === 'history' || saved === 'memory') ? saved : 'new'));
}

function consultSubLocal() {
  try { return localStorage.getItem('openyou_last_csub') || ''; } catch(e) { return ''; }
}

function bindConsultSubtabs() {
  const bar = $('#consultsubtabs');
  if (!bar || bar._bound) return;
  bar._bound = true;
  bar.addEventListener('click', function(e) {
    const tab = e.target.closest('.contextsubtab');
    if (!tab) return;
    bar.querySelectorAll('.contextsubtab').forEach(t => t.classList.remove('active'));
    tab.classList.add('active');
    loadConsultSub(tab.dataset.ksub);
  });
}

function loadConsultSub(sub) {
  try { localStorage.setItem('openyou_last_csub', sub || 'new'); } catch(e) {}
  ['new', 'history', 'memory'].forEach(s => {
    const el = $('#consult-' + s);
    if (el) el.style.display = (s === sub) ? 'flex' : 'none';
  });
  const bar = $('#consultsubtabs');
  if (bar) {
    bar.querySelectorAll('.contextsubtab').forEach(t => t.classList.toggle('active', t.dataset.ksub === sub));
  }
  if (sub === 'new') { renderChatMessages(); scrollChatBottom(); }
  if (sub === 'history') renderSessionList();
  if (sub === 'memory') {
    loadContextMemories();
    const ms = $('#memory-search');
    if (ms && !ms._bound) {
      ms._bound = true;
      ms.addEventListener('input', function() { loadContextMemories(this.value); });
    }
  }
}

function renderTimePage(sub) {
  bindTimeSubtabs();
  // explicit sub wins over persisted: the Home dashboard's Tasks card always lands on the task list.
  loadTimeSub(sub || loadTimeSubLocal() || 'star');
}

function bindTimeSubtabs() {
  const bar = $('#timesubtabs');
  if (!bar) return;
  if (!bar._bound) {
    bar._bound = true;
    bar.addEventListener('click', function(e) {
      const tab = e.target.closest('.contextsubtab');
      if (!tab) return;
      bar.querySelectorAll('.contextsubtab').forEach(t => t.classList.remove('active'));
      tab.classList.add('active');
      loadTimeSub(tab.dataset.ksub);
    });
  }
  const saved = loadTimeSubLocal();
  if (saved) {
    const el = document.querySelector('#timesubtabs .contextsubtab[data-ksub="' + saved + '"]');
    if (el) {
      bar.querySelectorAll('.contextsubtab').forEach(t => t.classList.remove('active'));
      el.classList.add('active');
    }
  }
}

function loadTimeSubLocal() {
  try { return localStorage.getItem('openyou_last_tsub') || ''; } catch(e) { return ''; }
}

function loadTimeSub(sub) {
  try { localStorage.setItem('openyou_last_tsub', sub || 'star'); } catch(e) {}
  ['star', 'task', 'calendar'].forEach(s => {
    const el = $('#time-' + s);
    if (el) el.style.display = (s === sub) ? '' : 'none';
  });
  // keep tab highlight in sync with content, else highlight lags after programmatic switches.
  const bar = $('#timesubtabs');
  if (bar) {
    bar.querySelectorAll('.contextsubtab').forEach(t => t.classList.toggle('active', t.dataset.ksub === sub));
  }
  if (sub === 'star') { /* Do button is static; nothing to load */ }
  if (sub === 'task') loadTasks();
  if (sub === 'calendar') loadCalendar();
}

let calYear = 0, calMonth = 0;
function loadCalendar() {
  const title = $('#cal-title');
  const grid = $('#cal-grid');
  if (!title || !grid) return;
  if (!calYear) { const now = new Date(); calYear = now.getFullYear(); calMonth = now.getMonth(); }
  title.textContent = calYear + '年' + (calMonth + 1) + '月';
  const weekdays = ['一','二','三','四','五','六','日'];
  let html = '<div style="display:grid;grid-template-columns:repeat(7,1fr);gap:2px;margin-bottom:4px">';
  weekdays.forEach(d => { html += '<div style="text-align:center;font-size:var(--fs-xs);color:var(--text-hint)">' + d + '</div>'; });
  html += '</div>';
  fetch('/api/tasks').then(r => r.json()).catch(() => ({ tasks: [] }))
  .then(function(resp) {
    const tasks = (resp && resp.tasks) || [];
    const byDate = {};
    tasks.forEach(t => {
      if (!t.end_date) return;
      const d = t.end_date.slice(0, 10);
      if (!byDate[d]) byDate[d] = [];
      byDate[d].push({ text: t.title, done: t.done, priority: t.priority });
    });
    const first = new Date(calYear, calMonth, 1);
    const startDow = (first.getDay() + 6) % 7;
    const daysInMonth = new Date(calYear, calMonth + 1, 0).getDate();
    const today = new Date();
    const todayStr = today.getFullYear() + '-' + String(today.getMonth() + 1).padStart(2, '0') + '-' + String(today.getDate()).padStart(2, '0');
    let cells = '';
    for (let i = 0; i < startDow; i++) cells += '<div style="min-height:56px"></div>';
    for (let d = 1; d <= daysInMonth; d++) {
      const ds = calYear + '-' + String(calMonth + 1).padStart(2, '0') + '-' + String(d).padStart(2, '0');
      const items = byDate[ds] || [];
      const isToday = ds === todayStr;
      cells += '<div onclick="calDayClick(\'' + ds + '\')" style="min-height:56px;border:1px solid var(--border);border-radius:6px;padding:2px 4px;overflow:hidden;cursor:pointer;' +
        (isToday ? 'background:var(--accent-soft, rgba(96,125,139,.12))' : '') + '">' +
        '<div style="font-size:var(--fs-xs);font-weight:' + (isToday ? '700' : '400') + '">' + d + (isToday ? ' ·今' : '') + '</div>';
      items.slice(0, 2).forEach(it => {
        const color = it.done ? 'var(--text-hint)' : 'var(--accent)';
        cells += '<div style="font-size:10px;line-height:1.2;color:' + color + ';white-space:nowrap;overflow:hidden;text-overflow:ellipsis">' +
          escHtml(it.text) + '</div>';
      });
      if (items.length > 2) cells += '<div style="font-size:9px;color:var(--text-hint)">+' + (items.length - 2) + '</div>';
      cells += '</div>';
    }
    grid.innerHTML = '<div style="display:grid;grid-template-columns:repeat(7,1fr);gap:2px">' + cells + '</div>';
  }).catch(() => { grid.innerHTML = '<div class="empty-state">Failed to load.</div>'; });
}

function calMove(dir) {
  calMonth += dir;
  if (calMonth < 0) { calMonth = 11; calYear--; }
  if (calMonth > 11) { calMonth = 0; calYear++; }
  loadCalendar();
}

function renderSkillsPage() {
  const container = $('#skills-list');
  if (!container) return;
  container.innerHTML = '<div class="loading">···</div>';
  fetch('/api/skills')
    .then(r => r.json())
    .then(data => {
      const skills = data.skills || [];
      if (skills.length === 0) {
        container.innerHTML = '<div class="empty-state" style="flex-direction:column;gap:8px">' +
          '<div>No skills yet.</div>' +
          '<div style="font-size:var(--fs-sm);color:var(--text-hint);max-width:280px;text-align:center">' +
          'Skills analyze problems from specific angles.<br>Tap + to create one.</div>' +
          '</div>';
      } else {
        let html = '';
        skills.forEach(s => {
          const triggers = (s.triggers || []).join(', ');
          const isStandard = !!(s.description || (s.triggers && s.triggers.length));
          const infoHtml = isStandard
            ? (s.description ? '<div class="doc-preview">' + escHtml(s.description) + '</div>' : '') +
              (triggers ? '<div class="skill-triggers">' + triggers.split(', ').map(t => '<span>' + escHtml(t) + '</span>').join('') + '</div>' : '')
            : (s.description ? '<div class="doc-preview">' + escHtml(s.description) + '</div>' : '');
          html += '<div class="skill-card" data-name="' + escHtml(s.name) + '" style="position:relative;overflow:hidden">' +
            '<div class="swipe-bg"><span>Delete</span></div>' +
            '<div class="doc-path">' + escHtml(s.path || s.name) + '</div>' +
            infoHtml +
            '</div>';
        });
        container.innerHTML = html;
        container.querySelectorAll('.skill-card').forEach(card => {
          let sx = 0, sy = 0;
          card.addEventListener('touchstart', e => { sx = e.touches[0].clientX; sy = e.touches[0].clientY; }, {passive:true});
          card.addEventListener('touchmove', e => {
            const dx = e.touches[0].clientX - sx, dy = e.touches[0].clientY - sy;
            if (Math.abs(dx) > Math.abs(dy) && Math.abs(dx) > 5) {
              if (dx < -30) card.classList.add('swiped'); else if (dx > 10) card.classList.remove('swiped');
            }
          }, {passive:true});
          card.addEventListener('touchend', () => {
            if (card.classList.contains('swiped')) { clearTimeout(card._st); card._st = setTimeout(() => card.classList.remove('swiped'), 1200); }
          });
          card.addEventListener('click', e => {
            if (card.classList.contains('swiped')) {
              if (e.target.closest('.swipe-bg')) {
                e.stopPropagation();
                fetch('/api/skills/' + encodeURIComponent(card.dataset.name), {method:'DELETE'})
                  .then(r => r.json())
                  .then(() => { showToast('Deleted'); renderSkillsPage(); })
                  .catch(() => showToast('Delete failed', 'warn'));
              }
              card.classList.remove('swiped'); clearTimeout(card._st); return;
            }
            openSkillEdit(card.dataset.name, skills, renderSkillsPage);
          });
        });
      }
      // rebind the + button each render to capture the current skills list.
      const addBtn = $('#btn-add-skill');
      if (addBtn) {
        const newBtn = addBtn.cloneNode(true);
        addBtn.parentNode.replaceChild(newBtn, addBtn);
        newBtn.addEventListener('click', () => openSkillEdit(null, skills, renderSkillsPage));
      }
    })
    .catch(() => { container.innerHTML = '<div class="empty-state">Failed to load skills.</div>'; });
}

function openSkillEdit(name, skills, refreshFn) {
  _skillsRefreshFn = refreshFn || null;
  const skill = name ? (skills || []).find(s => s.name === name) : null;
  const isNew = !skill;
  const container = $('#skills-list');
  if (!container) return;

  container.innerHTML =
    '<div style="display:flex;flex-direction:column;height:100%">' +
    '<div style="display:flex;align-items:center;gap:8px;padding:4px 0 8px;border-bottom:1px solid var(--border)">' +
      '<button class="btn btn-cancel" id="btn-skill-back" title="Back">←</button>' +
      '<span style="flex:1;font-size:var(--fs-md);font-weight:500;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' + (isNew ? 'New Skill' : escHtml(skill.name)) + '</span>' +
      (!isNew ? '<button class="btn btn-danger" id="btn-skill-del" style="font-size:var(--fs-sm);padding:4px 10px">Delete</button>' : '') +
      '<button class="btn btn-save" id="btn-skill-save">Save</button>' +
    '</div>' +
    (isNew ? '<div class="setting-row" style="padding:8px 0"><label style="font-size:var(--fs-md);color:var(--text-dim);min-width:80px">Name</label><input type="text" id="se-name" placeholder="skill-name" style="flex:1;padding:6px 8px;background:var(--bg-input);color:var(--text);border:1px solid var(--border);border-radius:var(--radius-sm);font-size:var(--fs-md)"></div>' : '') +
    '<div class="setting-row" style="padding:8px 0"><label style="font-size:var(--fs-md);color:var(--text-dim);min-width:80px">Description</label><input type="text" id="se-desc" placeholder="What this skill does" style="flex:1;padding:6px 8px;background:var(--bg-input);color:var(--text);border:1px solid var(--border);border-radius:var(--radius-sm);font-size:var(--fs-md)"></div>' +
    '<div class="setting-row" style="padding:8px 0"><label style="font-size:var(--fs-md);color:var(--text-dim);min-width:80px">Triggers</label><input type="text" id="se-triggers" placeholder="career, conflict" style="flex:1;padding:6px 8px;background:var(--bg-input);color:var(--text);border:1px solid var(--border);border-radius:var(--radius-sm);font-size:var(--fs-md)"></div>' +
    '<textarea id="se-content" placeholder="Skill content (Markdown)..." style="flex:1;width:100%;margin-top:8px;padding:8px;background:var(--bg-input);color:var(--text);border:1px solid var(--border);border-radius:var(--radius-sm);font-family:var(--font-mono);font-size:var(--fs-md);line-height:1.6;resize:none"></textarea>' +
    '</div>';

  if (skill) {
    const descEl = $('#se-desc'); if (descEl) descEl.value = skill.description || '';
    const trigEl = $('#se-triggers'); if (trigEl) trigEl.value = (skill.triggers || []).join(', ');
    fetch('/api/skills/' + encodeURIComponent(skill.name))
      .then(r => r.json())
      .then(d => { const c = $('#se-content'); if (c) c.value = d.content || ''; })
      .catch(() => {});
  }

  $('#btn-skill-back').addEventListener('click', () => { if (_skillsRefreshFn) _skillsRefreshFn(); else renderSkillsPage(); });

  $('#btn-skill-save').addEventListener('click', () => {
    const nm = isNew ? (($('#se-name') || {}).value || '').trim() : skill.name;
    if (!nm) { showToast('Name required', 'warn'); return; }
    const desc = (($('#se-desc') || {}).value || '').trim();
    const trig = (($('#se-triggers') || {}).value || '').split(',').map(t => t.trim()).filter(Boolean);
    const cont = (($('#se-content') || {}).value || '').trim();
    const url = isNew ? '/api/skills' : '/api/skills/' + encodeURIComponent(skill.name);
    fetch(url, {method: isNew ? 'POST' : 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify({name: nm, description: desc, triggers: trig, content: cont})})
      .then(r => r.json())
      .then(d => {
        if (d.ok || d.name) { showToast(isNew ? 'Skill created' : 'Skill saved'); if (_skillsRefreshFn) _skillsRefreshFn(); else renderSkillsPage(); }
        else showToast('Save failed: ' + (d.error || ''), 'warn');
      }).catch(() => showToast('Save failed', 'warn'));
  });

  const delBtn = $('#btn-skill-del');
  if (delBtn) delBtn.addEventListener('click', () => {
    if (!confirm('Delete skill "' + skill.name + '"?')) return;
    fetch('/api/skills/' + encodeURIComponent(skill.name), {method:'DELETE'})
      .then(r => r.json())
      .then(() => { showToast('Skill deleted'); if (_skillsRefreshFn) _skillsRefreshFn(); else renderSkillsPage(); })
      .catch(() => showToast('Delete failed', 'warn'));
  });
}

let _brProgressTimer = null;

let _skillsRefreshFn = null;
