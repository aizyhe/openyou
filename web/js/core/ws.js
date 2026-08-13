function ensureWebSocket() {
  if (state.ws && (state.ws.readyState === WebSocket.OPEN || state.ws.readyState === WebSocket.CONNECTING)) {
    return;
  }
  connectWebSocket();
}

function connectWebSocket() {
  if (state.ws) {
    state.ws.onclose = null;
    state.ws.onerror = null;
    state.ws.onmessage = null;
    try { state.ws.close(); } catch(e) {}
  }

  const url = 'ws://127.0.0.1:8765/ws';
  updateConnStatus('connecting');
  try {
    state.ws = new WebSocket(url);
  } catch (e) {
    scheduleReconnect();
    return;
  }

  state.ws.onopen = function() {
    state.isConnected = true;
    state.wsReconnectDelay = 1000;
    updateConnStatus('connected');
    showToast('Connected', '');
    setTimeout(() => { const t = $('#toast'); t.classList.remove('show'); }, 1500);

    flushSendQueue();
    sendWS({ type: 'sync-sessions' });

    // Daemon is the source of truth for api_key/model/base_url.
    syncDaemonConfig();
  };

  state.ws.onmessage = function(event) {
    handleWSMessage(event.data);
  };

  state.ws.onclose = function(event) {
    state.isConnected = false;
    updateConnStatus('disconnected');
    if (event.code !== 1000) {
      scheduleReconnect();
    }
  };

  state.ws.onerror = function() {
    state.isConnected = false;
    updateConnStatus('disconnected');
  };
}

function sendWS(data) {
  if (data.type === 'chat') {
    // FE-8: cancel any pending hide timer when a new request starts.
    clearTimeout(_brProgressTimer);
    _brProgressTimer = null;
    $('#br-progress').style.display = 'block';
    $('#br-skills-list').innerHTML = '';
    state.brainTrustSkills = [];
    state.llmSendTime = performance.now();
    // handleDone runs for both the "done" JSON event and the [DONE] sentinel
    // (analyze may emit more) — the guard makes only the first take effect.
    state._streamActive = true;
    state._streamGotContent = false;
    state._streamBackgrounded = false;
    // Baseline the suggested-memory count NOW so the post-stream notice only
    // counts memories created by THIS conversation (0 would report all existing).
    fetch('/api/memory').then(r => r.json()).then(d => {
      const all = (d && d.memories) || [];
      state._lastSuggestedCount = all.filter(m => m.suggested && !m.confirmed).length;
    }).catch(() => {});
    fetch('/api/analyze', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ message: data.message, session_id: data.session_id || state.currentSessionId })
    }).then(r => {
      if (!r.ok) {
        // handleError does the full cleanup — duplicating part of it here
        // left a blank bubble persisted and a dirty streamingSessionId behind.
        return r.text().then(t => handleError({ error: t || ('HTTP ' + r.status) }));
      }
      const reader = r.body.getReader();
      const decoder = new TextDecoder();
      let buf = '';
      function pump() {
        reader.read().then(({done, value}) => {
          if (done) { handleDone({}); return; }
          buf += decoder.decode(value, {stream: true});
          const lines = buf.split('\n');
          buf = lines.pop();
          for (const line of lines) {
            if (line.startsWith('data: ')) {
              const payload = line.slice(6);
              if (payload === '[DONE]') { handleDone({}); return; }
              handleWSMessage(payload);
            }
          }
          pump();
        }).catch(e => {
          // If the stream died while backgrounded (screen off: ArkWeb suspends
          // the network) and nothing arrived, that's power-saving — finalize quietly.
          if (!state._streamGotContent && state._streamBackgrounded) {
            state._streamActive = false;
            setStreaming(false);
            showThinking(false);
            $('#br-progress').style.display = 'none';
            state.brainTrustSkills = [];
            const sid = state.streamingSessionId || state.currentSessionId;
            const s = getSession(sid);
            if (s) {
              const last = s.messages[s.messages.length - 1];
              if (last && last.role === 'assistant' && !last.content) {
                s.messages.pop();
                saveSessions();
              }
            }
            state.streamingSessionId = null;
            if (sid === state.currentSessionId) renderChatMessages();
            return;
          }
          setStreaming(false);
          handleDone({interrupted: true});
        });
      }
      pump();
    }).catch(e => {
      handleError({ error: 'Consult error: ' + e.message });
    });
    return;
  }
  if (state.ws && state.ws.readyState === WebSocket.OPEN) {
    state.ws.send(JSON.stringify(data));
  } else if (data.type === 'sync-sessions') {
    scheduleReconnect();
  }
}

function scheduleReconnect() {
  if (state.wsReconnectTimer) return;
  updateConnStatus('connecting');
  state.wsReconnectTimer = setTimeout(() => {
    state.wsReconnectTimer = null;
    state.wsReconnectDelay = Math.min(state.wsReconnectDelay * 2, 5000);
    connectWebSocket();
  }, state.wsReconnectDelay);
}

function updateConnStatus(status) {
  const el = $('#conn-status');
  el.className = 'conn-status ' + status;
  if (status === 'disconnected') {
    el.textContent = 'Reconnecting...';
  } else if (status === 'connecting') {
    el.textContent = 'Connecting...';
  }
}

function handleWSMessage(raw) {
  let data;
  try { data = JSON.parse(raw); } catch(e) { return; }

  switch (data.type) {
    case 'text-delta':
      handleTextDelta(data);
      break;
    case 'tool-call':
      handleToolCall(data);
      break;
    case 'tool-result':
      handleToolResult(data);
      break;
    case 'done':
      handleDone(data);
      break;
    case 'source-citations':
      if (data.citations && Array.isArray(data.citations)) {
        state.lastCitations = data.citations;
      }
      break;
    case 'error':
      handleError(data);
      break;
    case 'sync-sessions':
      if (data.sessions && Array.isArray(data.sessions)) {
        data.sessions.forEach(ss => {
          if (ss.updated_at !== undefined && ss.updatedAt === undefined) ss.updatedAt = ss.updated_at;
          if (ss.created_at !== undefined && ss.createdAt === undefined) ss.createdAt = ss.created_at;
        });
        data.sessions.forEach(ss => {
          if (!getSession(ss.id)) {
            state.sessions.push(ss);
          }
        });
        state.sessions.sort((a, b) => (b.updatedAt || 0) - (a.updatedAt || 0));
        saveSessions();
        if (state.currentPage === 'consult') renderSessionList();
      }
      break;
    case 'diagnostics':
      handleDiagnostics(data);
      break;
    case 'memories-extracted':
      showToast(data.body || 'Memories extracted', 'info');
      if (state.currentPage === 'consult') loadContextMemories();
      break;
    case 'context-health':
      if (data.issues && data.issues.length > 0) {
        showToast('Context health: ' + data.issues.length + ' issue(s) found', 'alert');
        if (state.currentPage === 'context') renderContextPage();
      }
      break;
    case 'brain-trust-start':
      handleBrainTrustStart(data);
      break;
    case 'skill-progress':
      handleSkillProgress(data);
      break;
    case 'skill-result':
      handleSkillResult(data);
      break;
    case 'synthesis-delta':
      handleSynthesisDelta(data);
      break;
    case 'conflict':
      handleConflict(data);
      break;
    case 'context-loaded':
      handleContextLoaded(data);
      break;
    default:
      break;
  }
}

function handleBrainTrustStart(data) {
  showThinking(false);
  const skills = data.skills || [];
  state.brainTrustSkills = skills.map(s => ({ name: s.name || s, status: 'analyzing' }));
  renderBrainTrustBadges();
  // Bind the note to the streaming session, not whatever is on screen now.
  const s = getSession(state.streamingSessionId || state.currentSessionId);
  if (s) {
    const last = s.messages[s.messages.length - 1];
    if (last && last.role === 'assistant') {
      last.content = '**Brain Trust activated** — ' + skills.length + ' skills analyzing:\n' +
        skills.map(sk => '- ' + (sk.name || sk)).join('\n') + '\n\n';
    }
    saveSessions();
  }
}

function handleSkillProgress(data) {
  const skill = data.skill;
  const status = data.status || 'analyzing';
  const idx = state.brainTrustSkills.findIndex(s => s.name === skill);
  if (idx >= 0) state.brainTrustSkills[idx].status = status;
  renderBrainTrustBadges();
}

function handleSkillResult(data) {
  const skill = data.skill;
  const result = data.result || '';
  const sid = state.streamingSessionId || state.currentSessionId;

  if (sid === state.currentSessionId) {
    const container = $('#chat-messages');
    const existing = container.querySelector('.br-result-card[data-skill="' + cssAttrEscape(skill) + '"]');
    if (existing) {
      const body = existing.querySelector('.br-skill-body');
      if (body) body.textContent = result;
    } else {
      const card = document.createElement('div');
      card.className = 'br-result-card';
      card.dataset.skill = skill;
      card.innerHTML = '<div class="br-skill-name" onclick="this.parentElement.classList.toggle(\'expanded\')">' + escHtml(skill) + '</div>' +
        '<div class="br-skill-body">' + escHtml(result) + '</div>';
      container.appendChild(card);
    }
    scrollChatBottom(state.isStreaming);
  }
  const s = getSession(sid);
  if (s) {
    const last = s.messages[s.messages.length - 1];
    if (last && last.role === 'assistant') {
      last.content = (last.content || '') + '\n**' + skill + '**: ' + result + '\n';
    }
    saveSessions();
  }
}

function handleSynthesisDelta(data) {
  handleTextDelta({ text: data.text || '' });
}

function handleConflict(data) {
  const banner = document.createElement('div');
  banner.className = 'br-conflict';
  banner.innerHTML = '<div class="br-conflict-label">Disagreement: ' + escHtml(data.skill_a || 'Skill A') + ' vs ' + escHtml(data.skill_b || 'Skill B') + '</div>' +
    '<div class="br-conflict-desc">' + escHtml(data.description || '') + '</div>';
  $('#chat-messages').appendChild(banner);
  scrollChatBottom(state.isStreaming);
}

function handleContextLoaded(data) {
  state.contextData = {
    peopleNames: data.people_names || [],
    peopleCount: data.people_count || 0,
    rulesCount: data.rules_count || 0,
    memoryCount: data.memory_count || 0,
  };
  renderContextPills();
}

function renderContextPills(activePerson) {
  if (!activePerson) return;
  const container = $('#chat-context');
  if (!container) return;
  var ctx = state.contextData || {};
  var html = '<span class="ctx-label">Context:</span>';
  if (ctx.peopleCount > 0) {
    html += '<span class="ctx-pill person-pill' + (ctx.peopleNames && ctx.peopleNames.some(n => n === activePerson) ? ' active' : '') + '" role="button" tabindex="0">People <span class="ctx-count">' + ctx.peopleCount + '</span></span>';
  }
    if (ctx.rulesCount > 0) {
      html += '<span class="ctx-pill" role="button" tabindex="0">Rules <span class="ctx-count">' + ctx.rulesCount + '</span></span>';
    }
  if (ctx.memoryCount > 0) {
    html += '<span class="ctx-pill" role="button" tabindex="0">Memory <span class="ctx-count">' + ctx.memoryCount + '</span></span>';
  }
  container.innerHTML = html;
}

function checkPersonMention(text) {
  if (!state.contextData || !state.contextData.peopleNames || state.contextData.peopleNames.length === 0) return;
  const found = state.contextData.peopleNames.find(function(name) {
    return text.indexOf(name) !== -1;
  });
  renderContextPills(found || null);
}

function renderBrainTrustBadges() {
  const list = $('#br-skills-list');
  list.innerHTML = state.brainTrustSkills.map(s =>
    '<span class="br-skill-badge ' + escHtml(s.status) + '">' +
      escHtml(s.name) +
    '</span>'
  ).join('');
}

function handleTextDelta(data) {
  state._streamGotContent = true;
  showThinking(false);
  if (state.llmSendTime > 0) {
    const latency = Math.round(performance.now() - state.llmSendTime);
    state.llmSendTime = 0;
    state.llmLatencies.push(latency);
    if (state.llmLatencies.length > 10) state.llmLatencies.shift();
    const avg = Math.round(state.llmLatencies.reduce((a,b) => a+b, 0) / state.llmLatencies.length);
    updateSettingsDisplay('diag--latency', '~' + avg + 'ms');
  }
  const text = data.text || '';
  // Only touch the DOM when the streaming session is still displayed; binding
  // to streamingSessionId prevents cross-session bleed if the user switched away.
  const sid = state.streamingSessionId || state.currentSessionId;
  updateLastAssistantMsg(sid, text, undefined, undefined);
  if (sid === state.currentSessionId) appendTextDelta(text);
}

function handleToolCall(data) {
  showThinking(true);
  const sid = state.streamingSessionId || state.currentSessionId;
  const s = getSession(sid);
  if (!s) return;
  const last = s.messages[s.messages.length - 1];
  if (last && last.role === 'assistant') {
    last.tools = last.tools || [];
    const existing = last.tools.find(t => t.tool === data.tool && t.args === data.args);
    if (!existing) {
      last.tools.push({ tool: data.tool, name: data.tool, args: data.args, output: null });
    }
    saveSessions();

    if (sid !== state.currentSessionId) return;
    const bubbles = $$('.msg.assistant .msg-bubble', $('#chat-messages'));
    if (bubbles.length > 0) {
      const lastBubble = bubbles[bubbles.length - 1];
      const existingCard = lastBubble.querySelector(`.tool-card[data-tool="${cssAttrEscape(data.tool)}"]`);
      if (!existingCard) {
        const card = document.createElement('div');
        card.className = 'tool-card';
        card.dataset.tool = data.tool;
        card.innerHTML = `<span class="tool-label">${escHtml(data.tool)}</span>`;
        if (data.args) {
          const argsDiv = document.createElement('div');
          argsDiv.className = 'tool-args';
          argsDiv.textContent = typeof data.args === 'string' ? data.args : JSON.stringify(data.args);
          card.appendChild(argsDiv);
        }
        lastBubble.appendChild(card);
      }
    }
  }
}

function handleToolResult(data) {
  const sid = state.streamingSessionId || state.currentSessionId;
  const s = getSession(sid);
  if (!s) return;
  const last = s.messages[s.messages.length - 1];
  if (last && last.role === 'assistant' && last.tools) {
    const tool = last.tools.find(t => t.tool === data.tool);
    if (tool) {
      tool.output = data.output || data.result || '';
      if (data.error) tool.error = data.error;
    }
    saveSessions();

    if (sid !== state.currentSessionId) return;
    const bubbles = $$('.msg.assistant .msg-bubble', $('#chat-messages'));
    if (bubbles.length > 0) {
      const lastBubble = bubbles[bubbles.length - 1];
      const card = lastBubble.querySelector(`.tool-card[data-tool="${cssAttrEscape(data.tool)}"]`);
      if (card) {
        let outputDiv = card.querySelector('.tool-output');
        if (!outputDiv) {
          outputDiv = document.createElement('div');
          outputDiv.className = 'tool-output';
          card.appendChild(outputDiv);
        }
        if (data.error) {
          outputDiv.innerHTML = `<span style="color:var(--danger)">Error: ${escHtml(data.error)}</span>`;
        } else {
          const out = (data.output || data.result || '');
          outputDiv.textContent = typeof out === 'string' ? out.slice(0, 500) : '';
        }
      }
    }
  }
}

function handleDone(data) {
  // Daemon emits both a "done" event and a [DONE] sentinel per stream — only
  // the first run may act, or the memory notice double-posts on a wrong session.
  if (!state._streamActive) return;
  state._streamActive = false;
  setStreaming(false);
  state.currentStreamingText = '';
  // FE-6: keep home stats fresh — only when home is visible (the old
  // Object.assign was dead code that never refreshed anything).
  if (state.currentPage === 'home') loadHome();

  // FE-8: cancel any pending hide timer before setting a new one.
  clearTimeout(_brProgressTimer);
  _brProgressTimer = setTimeout(() => { $('#br-progress').style.display = 'none'; _brProgressTimer = null; }, 1000);
  state.brainTrustSkills = [];

  // Operate on the session the stream started for (the user may have switched).
  const sid = state.streamingSessionId || state.currentSessionId;
  const s = getSession(sid);
  if (s) {
    const last = s.messages[s.messages.length - 1];
    if (last && last.role === 'assistant') {
      // Drop an empty placeholder so a blank bubble isn't persisted/replayed.
      if (!last.content && !data.interrupted) {
        s.messages.pop();
        saveSessions();
        state.streamingSessionId = null;
        state.lastCitations = null;
        showThinking(false);
        if (sid === state.currentSessionId) renderChatMessages();
        return;
      }
      const citEntries = data.citations || state.lastCitations;
      if (citEntries) last.citations = citEntries;
      state.lastCitations = null;
      last.interrupted = !!data.interrupted;
      saveSessions();
    }
  }
  state.streamingSessionId = null;

  showThinking(false);

  if (!s || sid !== state.currentSessionId) return;

  const bubbles = $$('.msg.assistant .msg-bubble', $('#chat-messages'));
  if (bubbles.length > 0) {
    const lastBubble = bubbles[bubbles.length - 1];
    const lastMsg = s.messages[s.messages.length - 1];
    if (lastMsg && lastMsg.citations && lastMsg.citations.length > 0) {
      let citDiv = lastBubble.querySelector('.citations');
      if (!citDiv) {
        citDiv = document.createElement('div');
        citDiv.className = 'citations';
        const label = document.createElement('div');
        label.className = 'citation-label';
        label.textContent = 'Sources:';
        citDiv.appendChild(label);
        lastBubble.appendChild(citDiv);
      }
      citDiv.querySelectorAll('.citation-item').forEach(el => el.remove());
      lastMsg.citations.forEach(c => {
        const item = document.createElement('div');
        item.className = 'citation-item cite-' + (c.from || 'person');
        item.setAttribute('role', 'button');
        item.setAttribute('tabindex', '0');
        item.textContent = c.title || c.source || c.url || '';
        if (c.url) item.title = c.url;
        citDiv.appendChild(item);
      });
    }

    const parentMsg = lastBubble.closest('.msg');
    if (parentMsg) {
      let timeEl = parentMsg.querySelector('.msg-time');
      if (!timeEl) {
        timeEl = document.createElement('div');
        timeEl.className = 'msg-time';
        parentMsg.appendChild(timeEl);
      }
      let timeText = formatTime(lastMsg.time);
      if (lastMsg.interrupted) timeText += ' (interrupted)';
      timeEl.textContent = timeText;
    }
  }

  if (data.interrupted) {
    $('#retry-banner').classList.add('show');
  }

  flushSendQueue();
  scrollChatBottom(state.isStreaming);

  if (!data.interrupted) {
    const prevSuggestedCount = state._lastSuggestedCount || 0;
    setTimeout(function() {
      fetch('/api/memory')
        .then(function(r) { return r.json(); })
        .then(function(d) {
          const all = d.memories || [];
          const suggested = all.filter(function(m) { return m.suggested && !m.confirmed; });
          state._lastSuggestedCount = suggested.length;
          const newCount = suggested.length - prevSuggestedCount;
          if (newCount > 0) {
            const chatMsgs = $('#chat-messages');
            if (!chatMsgs) return;
            const notice = document.createElement('div');
            notice.className = 'memory-notice';
            // Memory lives on the Consult page's "memory" sub-tab; the old ('context','memories') target matched nothing.
            notice.innerHTML = 'Extracted ' + newCount + ' ' + (newCount === 1 ? 'memory' : 'memories') + '. <span onclick="navigateTo(\'consult\',\'memory\')" style="color:var(--accent-text);cursor:pointer">Review →</span>';
            chatMsgs.appendChild(notice);
            scrollChatBottom(state.isStreaming);
          }
        })
        .catch(function() {});
    }, 2000);
  }
}

function handleError(data) {
  // Consume the stream so any done/[DONE] after an error can't re-run cleanup.
  state._streamActive = false;
  setStreaming(false);
  state.currentStreamingText = '';
  showThinking(false);
  $('#br-progress').style.display = 'none';
  state.brainTrustSkills = [];
  showToast('Error: ' + (data.error || data.message || 'Unknown error'), 'warn');
  $('#retry-banner').classList.add('show');

  const sid = state.streamingSessionId || state.currentSessionId;
  const s = getSession(sid);
  if (s) {
    const last = s.messages[s.messages.length - 1];
    if (last && last.role === 'assistant') {
      if (!last.content) {
        // Nothing streamed — drop the empty placeholder, not leave a blank bubble.
        s.messages.pop();
      } else {
        last.interrupted = true;
      }
      saveSessions();
    }
  }
  state.streamingSessionId = null;
  if (sid === state.currentSessionId) {
    renderChatMessages();
    scrollChatBottom(state.isStreaming);
  }
}

function updateSettingsDisplay(key, value) {
  const row = document.querySelector('.setting-row[data-key="' + key + '"] .setting-disabled');
  if (row) row.textContent = value;
}

async function testConnection() {
  updateSettingsDisplay('diag--status', 'Testing...');
  updateSettingsDisplay('diag--latency', '...');

  // Health check only — the old /api/chat "ping" created a permanent session,
  // burned an LLM call, and later triggered follow-up. Diagnostics must be side-effect-free.
  const healthStart = performance.now();
  try {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), 10000);
    const r = await fetch('/api/health', { signal: ctrl.signal });
    clearTimeout(timer);
    if (!r.ok) { throw new Error('HTTP ' + r.status); }
    const healthLatency = Math.round(performance.now() - healthStart);
    state.daemonOnline = true;
    updateSettingsDisplay('diag--status', 'Online (' + healthLatency + 'ms)');
    updateSettingsDisplay('diag--latency', healthLatency + 'ms');
    showToast('Connected · Health ' + healthLatency + 'ms');
  } catch (e) {
    state.daemonOnline = false;
    updateSettingsDisplay('diag--status', 'Offline');
    showToast('Connection failed: ' + (e.name === 'AbortError' ? 'timeout' : e.message), 'error');
  }
}

function handleDiagnostics(data) {
  if (state.currentPage === 'settings') {
    updateSettingsDisplay('diag--status', data.status || 'Connected');
    if (data.latencyMs !== undefined) updateSettingsDisplay('diag--latency', data.latencyMs + 'ms');
    if (data.sessions_count !== undefined) updateSettingsDisplay('diag--sessions', '' + data.sessions_count);
    if (data.memories_count !== undefined) updateSettingsDisplay('diag--memories', '' + data.memories_count);
    if (data.people_count !== undefined) updateSettingsDisplay('diag--people', '' + data.people_count);
  }
}
