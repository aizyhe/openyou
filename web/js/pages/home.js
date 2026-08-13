function loadHome() {
  Promise.all([
    fetch('/api/home').then(r => r.json()).catch(() => null),
    fetch('/api/docs').then(r => r.json()).catch(() => [])
  ]).then(([data, docs]) => {
    if (!data) { $('#home-scroll').innerHTML = '<div class="empty-state">Waiting for daemon...</div>'; return; }
    var docCounts = { insides: 0, rules: 0, skills: 0 };
    (docs || []).forEach(function(d) {
      if (d.path.indexOf('inside/') === 0) docCounts.insides++;
      else if (d.path.indexOf('rule/') === 0) docCounts.rules++;
      else if (d.path.indexOf('skill/') === 0) docCounts.skills++;
    });
    let html = '';

    const memPending = data.memory_pending || 0;
    const feedback = data.pending_feedback || [];
    if (memPending > 0 || feedback.length > 0) {
      html += '<div class="home-card"><h3><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="var(--accent)" stroke-width="2" stroke-linecap="round" style="margin-right:6px"><path d="M12 2C6.477 2 2 6.477 2 12s4.477 10 10 10 10-4.477 10-10S17.523 2 12 2z"/><path d="M12 16v-4m0-4h.01"/></svg>Needs You</h3>';
      if (memPending > 0) {
        html += '<div class="alert-item" onclick="navigateToMemories()" style="cursor:pointer">';
        html += '<span class="alert-dot" style="color:var(--accent)">●</span>';
        html += '<span>Memory · ' + memPending + ' unconfirmed</span></div>';
      }
      feedback.forEach(fb => {
        // Value is a JS literal inside an HTML attribute — needs both layers (backup ids aren't sanitized).
        html += '<div class="alert-item" onclick="openSession(\'' + escHtml(jsStr(fb.session_id)) + '\')" style="cursor:pointer">';
        html += '<span class="alert-dot" style="color:var(--accent)">●</span>';
        html += '<span>' + escHtml(fb.title || 'Consult') + ' · ' + fb.days + 'd ago</span></div>';
      });
      html += '</div>';
    }

    html += '<div class="home-card"><h3><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" style="margin-right:6px"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>Time</h3>';
    html += '<div class="context-stat">';
    // Always land on the task list, never the last-used sub-tab (e.g. calendar).
    html += '<div class="stat-item stat-link" onclick="switchTab(\'time\',\'task\')"><div class="stat-num">' + (data.task_count || 0) + '</div><div class="stat-label">Task</div></div>';
    html += '</div></div>';

    html += '<div class="home-card"><h3><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" style="margin-right:6px"><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/></svg>Context</h3>';
    html += '<div class="context-stat">';
    html += '<div class="stat-item stat-link" onclick="navigateTo(\'context\',\'inside\')"><div class="stat-num">' + docCounts.insides + '</div><div class="stat-label">Inside</div></div>';
    html += '<div class="stat-item stat-link" onclick="navigateTo(\'context\',\'rule\')"><div class="stat-num">' + docCounts.rules + '</div><div class="stat-label">Rule</div></div>';
    html += '<div class="stat-item stat-link" onclick="navigateTo(\'context\',\'skill\')"><div class="stat-num">' + docCounts.skills + '</div><div class="stat-label">Skill</div></div>';
    html += '</div></div>';

    html += '<div class="home-card"><h3><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" style="margin-right:6px"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>Consult</h3>';
    html += '<div class="context-stat">';
    // History goes to the history sub-tab — never the new-chat view.
    html += '<div class="stat-item stat-link" onclick="switchTab(\'consult\',\'history\')"><div class="stat-num">' + (data.session_count || 0) + '</div><div class="stat-label">History</div></div>';
    html += '<div class="stat-item stat-link" onclick="navigateTo(\'consult\',\'memory\')"><div class="stat-num">' + (data.memory_count || 0) + '</div><div class="stat-label">Memory</div></div>';
    html += '</div></div>';

    if (state.sessions.length > 30) {
      html += '<div class="home-card" style="border-color:var(--warning-bg)"><p style="font-size:var(--fs-sm);color:var(--text-dim)">' + state.sessions.length + ' sessions — back up in Settings.</p></div>';
    }

    $('#home-scroll').innerHTML = html;
  }).catch(() => { $('#home-scroll').innerHTML = '<div class="empty-state">Waiting for daemon...</div>'; });
}

function navigateToMemories() {
  navigateTo('consult', 'memory');
}

// Render the target sub immediately — avoids the default-sub-then-switch flash on every dashboard jump.
function navigateTo(tab, sub) {
  switchTab(tab, sub);
}
