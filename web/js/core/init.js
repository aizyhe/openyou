
function syncSessionListFromServer() {
  fetch('/api/sessions')
    .then(r => r.ok ? r.json() : null)
    .then(data => {
      if (!data || !Array.isArray(data.sessions)) return;
      let changed = false;
      data.sessions.forEach(ss => {
        const updatedAt = ss.updated_at ? Math.round(ss.updated_at / 1e6) : 0;
        const createdAt = ss.created_at ? Math.round(ss.created_at / 1e6) : 0;
        const existing = getSession(ss.id);
        if (!existing) {
          state.sessions.push({
            id: ss.id,
            title: ss.title || 'Consult',
            messages: [],
            createdAt,
            updatedAt,
          });
          changed = true;
        } else if (!existing.title && ss.title) {
          existing.title = ss.title;
          changed = true;
        }
      });
      if (changed) {
        state.sessions.sort((a, b) => (b.updatedAt || 0) - (a.updatedAt || 0));
        saveSessions();
        if (state.currentPage === 'consult') renderSessionList();
      }
    })
    .catch(() => {}); // daemon offline — skip silently
}

function init() {
  loadSettings();
  loadSessions();
  renderSessionList();
  initSessionListTouch();
  initChatTouch();

  // Fallback for older devices where adjustResize doesn't move the input: scroll it into view.
  if (window.visualViewport) {
    window.visualViewport.addEventListener('resize', function() {
      var active = document.activeElement;
      if (active && active.closest('.chat-input-area')) {
        active.scrollIntoView({ block: 'end', behavior: 'instant' });
      }
    });
  }

  document.querySelectorAll('.tab-btn').forEach(btn => {
    btn.addEventListener('click', () => switchTab(btn.dataset.tab));
  });

  const addBtn = $('#btn-task-add');
  if (addBtn) addBtn.addEventListener('click', addTask);
  const titleEl = $('#task-add-title');
  if (titleEl) titleEl.addEventListener('keydown', e => { if (e.key === 'Enter') addTask(); });
  const doBtn = $('#btn-star');
  if (doBtn) doBtn.addEventListener('click', runStar);

  // Settings buttons are bound in consult.js (FE-7). Re-binding here fired both
  // handlers and init.js's showPage('home') overrode the FE-7 target. Do not re-bind.

  const backFromSkills = $('#btn-back-from-skills');
  if (backFromSkills) backFromSkills.addEventListener('click', () => showPage('context'));


  switchTab('home');

  // FE-3: restore last page/session/subtab (the context sub-tab restores itself).
  try {
    const lastTab = localStorage.getItem('openyou_last_tab');
    if (lastTab) {
      switchTab(lastTab);
      const lastSession = localStorage.getItem('openyou_last_session');
      if (lastTab === 'consult' && lastSession && getSession(lastSession)) {
        openSession(lastSession);
      }
    }
  } catch(e) {}

  // If a stream dies while backgrounded (screen off → ArkWeb suspends
  // networking), that's power-saving — the flag lets it finalize silently.
  document.addEventListener('visibilitychange', function() {
    if (document.hidden && state.isStreaming) state._streamBackgrounded = true;
  });

  ensureWebSocket();

  // Sync so sessions survive localStorage clears (runs silently after local render).
  syncSessionListFromServer();

  pollHealth();
  state._healthInterval = setInterval(pollHealth, 5000);

  loadDiagnosticsStats();
  state._statsInterval = setInterval(loadDiagnosticsStats, 30000);

}

function pollHealth() {
  fetch('/api/health')
    .then(r => r.json())
    .then(data => {
      state.daemonOnline = true;
      if (state.currentPage === 'settings') {
        updateSettingsDisplay('diag--status', 'Online');
      }
    })
    .catch(() => {
      state.daemonOnline = false;
      if (state.currentPage === 'settings') {
        updateSettingsDisplay('diag--status', 'Offline');
      }
    });
}

function loadDiagnosticsStats() {
  if (state.currentPage !== 'settings') return;
  updateSettingsDisplay('diag--sessions', '' + state.sessions.length);
  fetch('/api/home')
    .then(r => r.json())
    .then(data => {
      updateSettingsDisplay('diag--people', '' + (data.people_count || '0'));
    })
    .catch(() => {});
  fetch('/api/memory')
    .then(r => r.json())
    .then(data => {
      if (data && data.memories) {
        updateSettingsDisplay('diag--memories', '' + data.memories.length);
      }
    })
    .catch(() => {});
}

// Exposed for inline onclick handlers.
window.sendFeedback = sendFeedback;
window.submitCorrection = submitCorrection;
window.openSession = openSession;
window.showPage = showPage;
window.renderSessionList = renderSessionList;
window.closeModal = closeModal;
window.navigateToMemories = navigateToMemories;
window.closeStar = closeStar;
window.addStarSuggestionAsTask = addStarSuggestionAsTask;
window.acceptStarSuggestion = acceptStarSuggestion;
window.editStarSuggestion = editStarSuggestion;
window.dismissStarSuggestion = dismissStarSuggestion;
window.saveEditedStarSuggestion = saveEditedStarSuggestion;
window.navigateToConsult = navigateToConsult;
window.switchTab = switchTab;
window.calMove = calMove;
window.calDayClick = calDayClick;
window.clearCalFilter = clearCalFilter;
window.loadTimeSub = loadTimeSub;
window.navigateTo = navigateTo;
window.memAction = memAction;
window.openSkillEdit = openSkillEdit;
init();
