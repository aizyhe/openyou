function navigateToConsult() {
  if (!state.currentSessionId) {
    var empty = state.sessions.find(function(x) { return x.messages.length === 0; });
    var s = empty || createSession();
    state.currentSessionId = s.id;
    var titleEl = document.getElementById('chat-title');
    if (titleEl) titleEl.textContent = s.title;
  }
  document.querySelectorAll('.page').forEach(function(p) { p.classList.remove('active'); });
  var target = document.getElementById('consult-page');
  if (target) target.classList.add('active');
  document.querySelectorAll('.tab-btn').forEach(function(b) { b.classList.remove('active'); });
  var tabBtn = document.querySelector('.tab-btn[data-tab="consult"]');
  if (tabBtn) tabBtn.classList.add('active');
  try { localStorage.setItem('openyou_last_tab', 'consult'); } catch(e) {}
  state.currentPage = 'consult';
  renderChatMessages();
  scrollChatBottom(state.isStreaming /* force during streaming */);
}

// Pass sub straight to the renderer — avoids a wrong-sub flash.
function switchTab(tab, sub) {
  try { localStorage.setItem('openyou_last_tab', tab); } catch(e) {}
  document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
  const btn = document.querySelector('.tab-btn[data-tab="' + tab + '"]');
  if (btn) btn.classList.add('active');
  document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
  const pageMap = {
    home: 'home-page',
    time: 'time-page',
    context: 'context-page',
    'consult': 'consult-page'
  };
  const pageId = pageMap[tab];
  if (pageId) {
    const page = document.getElementById(pageId);
    if (page) { page.classList.add('active'); state.currentPage = tab === 'context' ? 'context' : tab; }
  }
  if (tab === 'home') showPage('home');
  if (tab === 'time') showPage('time', sub);
  if (tab === 'context') showPage('context', sub);
  if (tab === 'consult') {
    if (!state.currentSessionId) {
      // Reuse the empty session so re-entering the tab doesn't pile up blank "New Consult" ones.
      const empty = state.sessions.find(x => x.messages.length === 0);
      const s = empty || createSession();
      state.currentSessionId = s.id;
      $('#chat-title').textContent = s.title;
    }
    showPage('consult', sub);
  }
}
