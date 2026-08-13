// Rendering the target sub directly (instead of loading inside first) avoids
// the visible inside→target flash when arriving from the Home dashboard.
function renderContextPage(sub) {
  bindContextSubtabs();
  var target = sub;
  if (!target) {
    try { target = localStorage.getItem('openyou_last_ksub') || ''; } catch(e) {}
  }
  if (target !== 'inside' && target !== 'rule' && target !== 'skill') target = 'inside';
  var bar = $('#contextsubtabs');
  if (bar) {
    bar.querySelectorAll('.contextsubtab').forEach(function(t) {
      t.classList.toggle('active', t.dataset.ksub === target);
    });
  }
  try { localStorage.setItem('openyou_last_ksub', target); } catch(e) {}
  if (target === 'inside') loadContextInsides();
  else if (target === 'rule') loadContextRules();
  else loadContextSkills();
}

function bindContextSubtabs() {
  const bar = $('#contextsubtabs');
  if (!bar) return;
  if (!bar._bound) {
    bar._bound = true;
    bar.addEventListener('click', function(e) {
      const tab = e.target.closest('.contextsubtab');
      if (!tab) return;
      bar.querySelectorAll('.contextsubtab').forEach(t => t.classList.remove('active'));
      tab.classList.add('active');
      const sub = tab.dataset.ksub;
      try { localStorage.setItem('openyou_last_ksub', sub); } catch(e) {}
      if (sub === 'inside') loadContextInsides();
      if (sub === 'rule') loadContextRules();
      if (sub === 'skill') loadContextSkills();
    });
  }
}

function loadContextInsides() {
  var container = $('#context-content');
  container.innerHTML = '<div class="empty-state">···</div>';
  fetch('/api/docs')
    .then(function(r) { return r.json(); })
    .then(function(files) {
      var insides = files.filter(function(f) { return f.path.indexOf('inside/') === 0; });
      renderDocList(container, insides, 'inside');
    })
    .catch(function() { container.innerHTML = '<div class="empty-state">Failed to load.</div>'; });
}

function renderDocList(container, files, prefix) {
  if (!files) files = [];
  var html = '<div style="display:flex;gap:6px;margin-bottom:8px">' +
    '<input id="doc-add-input" placeholder="add... (dir/name.md)" autocomplete="off" style="flex:1;min-width:0;padding:6px 8px;background:var(--bg-input);color:var(--text);border:1px solid var(--border);border-radius:var(--radius-sm);font-size:var(--fs-md)">' +
    '<button id="btn-starc-add" class="add-btn">+</button>' +
    '</div>';
  if (files.length === 0) {
    html += '<div class="empty-state">No documents yet.</div>';
  } else {
    files.forEach(function(f) {
    html += '<div class="people-card doc-card" data-path="' + escHtml(f.path) + '" style="position:relative;overflow:hidden">' +
      '<div class="swipe-bg"><span>Delete</span></div>' +
      '<div class="doc-path">' + escHtml(f.path) + '</div>' +
      (f.preview ? '<div class="doc-preview">' + escHtml(f.preview) + '</div>' : '') +
    '</div>';
    });
  }
  container.innerHTML = html;
  if (files.length > 0) wireDocSwipe(container);
  var addBtn = $('#btn-starc-add');
  var addInp = $('#doc-add-input');
  function docAdd() {
    var val = (addInp.value || '').trim();
    if (!val) return;
    if (!val.endsWith('.md')) val += '.md';
    var path = (prefix || 'inside') + '/' + val.replace(/^\//, '');
    fetch('/api/docs/' + encodeURIComponent(path), {
      method: 'PUT', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({content: ''})
    }).then(function(r) { return r.json(); })
      .then(function() { addInp.value = ''; reloadDocTab(path); })
      .catch(function() { showToast('Failed to create', 'warn'); });
  }
  if (addBtn) addBtn.addEventListener('click', docAdd);
  if (addInp) addInp.addEventListener('keydown', function(e) { if (e.key === 'Enter') docAdd(); });
}

function wireDocSwipe(container) {
  container.querySelectorAll('.doc-card').forEach(function(el) {
    var sx = 0, sy = 0, sw = false;
    el.addEventListener('touchstart', function(e) { sx = e.touches[0].clientX; sy = e.touches[0].clientY; sw = false; }, {passive:true});
    el.addEventListener('touchmove', function(e) {
      var dx = e.touches[0].clientX - sx, dy = e.touches[0].clientY - sy;
      if (Math.abs(dx) > Math.abs(dy) && Math.abs(dx) > 5) {
        sw = true; if (dx < -30) el.classList.add('swiped'); else if (dx > 10) el.classList.remove('swiped');
      }
    }, {passive:true});
    el.addEventListener('touchend', function() {
      if (el.classList.contains('swiped')) { clearTimeout(el._st); el._st = setTimeout(function() { el.classList.remove('swiped'); }, 1200); }
    });
    el.addEventListener('click', function(e) {
      if (el.classList.contains('swiped')) {
        if (e.target.closest('.swipe-bg')) { e.stopPropagation(); deleteDoc(el.dataset.path); }
        el.classList.remove('swiped'); clearTimeout(el._st); return;
      }
      openDocEdit(el.dataset.path);
    });
  });
}

function reloadDocTab(path) {
  if (path.indexOf('inside/') === 0) loadContextInsides();
  else if (path.indexOf('rule/') === 0) loadContextRules();
  else if (path.indexOf('skill/') === 0) loadContextSkills();
  else loadContextInsides();
}

function openDocEdit(path) {
  fetch('/api/docs/' + encodeURIComponent(path))
    .then(function(r) { if (!r.ok) throw r.status; return r.text(); })
    .then(function(content) {
      var container = $('#context-content');
      container.innerHTML =
        '<div style="display:flex;flex-direction:column;height:100%">' +
        // Filename on its own line so long paths are never truncated by the buttons.
        '<div style="padding:6px 0 4px;font-size:var(--fs-md);font-weight:500;color:var(--accent-text);word-break:break-all">' + escHtml(path) + '</div>' +
        // Reading mode → no Save; there is nothing to save until edit mode.
        '<div style="display:flex;align-items:center;gap:8px;padding:0 0 8px;border-bottom:1px solid var(--border)">' +
          '<button class="btn btn-cancel" id="btn-starc-back" title="Back">←</button>' +
          '<span style="flex:1"></span>' +
          '<button class="btn btn-cancel" id="btn-starc-preview">Edit</button>' +
          '<button class="btn btn-save" id="btn-starc-save" style="display:none">Save</button>' +
        '</div>' +
        '<textarea id="kdoc-content" style="flex:1;width:100%;padding:8px;margin-top:6px;background:var(--bg-input);color:var(--text);border:1px solid var(--border);border-radius:var(--radius-sm);font-family:var(--font-mono);font-size:var(--fs-md);line-height:1.6;resize:none;display:none">' + escHtml(content) + '</textarea>' +
        '<div id="kdoc-preview" class="modal-markdown" style="flex:1;width:100%;padding:8px;margin-top:6px;overflow:auto"></div>' +
        '</div>';
      $('#btn-starc-back').addEventListener('click', function() {
        // Dirty check: leaving the editor must not silently discard typing.
        var ta = $('#kdoc-content');
        if (ta && ta.value !== content && !confirm('Discard unsaved changes?')) return;
        reloadDocTab(path);
      });
      wireMarkdownPreview('btn-starc-preview', 'kdoc-content', 'kdoc-preview', true, 'btn-starc-save');
      $('#btn-starc-save').addEventListener('click', function() {
        var self = this;
        self.disabled = true; self.textContent = 'Saving...';
        fetch('/api/docs/' + encodeURIComponent(path), {
          method: 'PUT', headers: {'Content-Type':'application/json'},
          body: JSON.stringify({content: $('#kdoc-content').value})
        }).then(function(r) { return r.json(); })
          .then(function() { showToast('Saved'); reloadDocTab(path); })
          .catch(function() { showToast('Save failed', 'warn'); self.disabled = false; self.textContent = 'Save'; });
      });
    })
    .catch(function() { showToast('Failed to load doc', 'warn'); reloadDocTab(path); });
}

function deleteDoc(path) {
  if (!confirm('Delete ' + path + '?')) return;
  fetch('/api/docs/' + encodeURIComponent(path), { method: 'DELETE' })
    .then(function(r) { return r.json(); })
    .then(function() { showToast('Deleted'); reloadDocTab(path); })
    .catch(function() { showToast('Delete failed', 'warn'); });
}

function loadContextRules() {
  var container = $('#context-content');
  container.innerHTML = '<div class="empty-state">···</div>';
  fetch('/api/docs')
    .then(function(r) { return r.json(); })
    .then(function(files) {
      var rules = files.filter(function(f) { return f.path.indexOf('rule/') === 0; });
      renderDocList(container, rules, 'rule');
    })
    .catch(function() { container.innerHTML = '<div class="empty-state">Failed to load.</div>'; });
}

function loadContextSkills() {
  // List only userdata/skill/ docs, not every matchable doc in the tree.
  var container = $('#context-content');
  container.innerHTML = '<div class="empty-state">···</div>';
  fetch('/api/skills')
    .then(function(r) { return r.json(); })
    .then(function(data) {
      var skills = ((data && data.skills) || []).filter(function(s) {
        return (s.path || '').indexOf('skill/') === 0;
      });
      var html = '<div style="display:flex;gap:6px;margin-bottom:8px">' +
        '<input id="doc-add-input" placeholder="add... (dir/name.md)" autocomplete="off" style="flex:1;min-width:0;padding:6px 8px;background:var(--bg-input);color:var(--text);border:1px solid var(--border);border-radius:var(--radius-sm);font-size:var(--fs-md)">' +
        '<button id="btn-starc-add" class="add-btn">+</button>' +
        '</div>';
      if (skills.length === 0) {
        html += '<div class="empty-state">No skill documents yet.</div>';
      } else {
        skills.forEach(function(s) {
          var preview = (s.description || s.content || '').replace(/\s*\n\s*/g, ' ').trim();
          if (preview.length > 300) preview = preview.slice(0, 300) + '…';
          var chips = (s.triggers || []).map(function(t) {
            return '<span>' + escHtml(t) + '</span>';
          }).join('');
          html += '<div class="skill-card doc-card" data-path="' + escHtml(s.path) + '" style="position:relative;overflow:hidden">' +
            '<div class="swipe-bg"><span>Delete</span></div>' +
            '<div class="doc-path">' + escHtml(s.path) + '</div>' +
            (preview ? '<div class="doc-preview">' + escHtml(preview) + '</div>' : '') +
            (chips ? '<div class="skill-triggers">' + chips + '</div>' : '') +
            '</div>';
        });
      }
      container.innerHTML = html;
      if (skills.length > 0) wireDocSwipe(container);
      var addBtn = $('#btn-starc-add');
      var addInp = $('#doc-add-input');
      function docAdd() {
        var val = (addInp.value || '').trim();
        if (!val) return;
        if (!val.endsWith('.md')) val += '.md';
        var path = 'skill/' + val.replace(/^\//, '');
        fetch('/api/docs/' + encodeURIComponent(path), {
          method: 'PUT', headers: {'Content-Type':'application/json'},
          body: JSON.stringify({content: ''})
        }).then(function(r) { return r.json(); })
          .then(function() { addInp.value = ''; loadContextSkills(); })
          .catch(function() { showToast('Failed to create', 'warn'); });
      }
      if (addBtn) addBtn.addEventListener('click', docAdd);
      if (addInp) addInp.addEventListener('keydown', function(e) { if (e.key === 'Enter') docAdd(); });
    })
    .catch(function() { container.innerHTML = '<div class="empty-state">Failed to load.</div>'; });
}

function loadContextMemories(filter) {
  var container = $('#consult-memory-list');
  if (!container) return;
  container.innerHTML = '<div class="empty-state">···</div>';
  fetch('/api/memory')
    .then(function(r) { return r.json(); })
    .then(function(data) {
      var memories = (data && data.memories) || [];
      if (filter && filter.trim()) {
        var q = filter.trim().toLowerCase();
        memories = memories.filter(function(m) {
          return (m.content || '').toLowerCase().includes(q) ||
                 (m.person || '').toLowerCase().includes(q) ||
                 (m.type || '').toLowerCase().includes(q);
        });
      }
      if (memories.length === 0) {
        container.innerHTML = '<div class="empty-state">No memories</div>';
        return;
      }
      memories.sort(function(a, b) {
        if (a.suggested && !b.suggested) return -1;
        if (!a.suggested && b.suggested) return 1;
        return (b.confidence || 0) - (a.confidence || 0);
      });
      var html = '';
      memories.forEach(function(m) {
        var statusColor = m.suggested ? 'var(--warning)' : m.disputed ? 'var(--danger)' : 'var(--success)';
        var statusLabel = m.suggested ? '待确认' : m.disputed ? '不准确' : '已确认';
        var confPct = Math.round((m.confidence || 0) * 100);
        html += '<div class="mem-card" data-id="' + escHtml(m.id) + '" style="position:relative;overflow:hidden;border-bottom:1px solid var(--border)">' +
          '<div class="swipe-bg"><span>Delete</span></div>' +
          '<div class="mem-card-inner" style="padding:10px 12px">' +
          '<div style="display:flex;align-items:center;gap:6px;margin-bottom:4px">' +
            '<span style="font-size:10px;color:' + statusColor + ';border:1px solid ' + statusColor + ';border-radius:4px;padding:1px 5px">' + statusLabel + '</span>' +
            '<span style="font-size:10px;color:var(--text-hint)">' + escHtml(m.type || '') + '</span>' +
            (m.person ? '<span style="font-size:10px;color:var(--text-hint)">· ' + escHtml(m.person) + '</span>' : '') +
            '<span style="font-size:10px;color:var(--text-hint);margin-left:auto">' + confPct + '%</span>' +
          '</div>' +
          '<div class="mem-content">' + escHtml(m.content || '') + '</div>' +
          '<div style="display:flex;gap:6px">' +
            // Two-layer escaping: browser decodes &#39; to ' before JS, breaking onclick (ids from imported backups).
            (m.suggested ? '<button class="btn-small" onclick="memAction(\'confirm\',\'' + escHtml(jsStr(m.id)) + '\')">确认</button>' : '') +
            '<button class="btn-small" onclick="memAction(\'dispute\',\'' + escHtml(jsStr(m.id)) + '\')">不准确</button>' +
            '<button class="btn-small danger" onclick="memAction(\'delete\',\'' + escHtml(jsStr(m.id)) + '\')">删除</button>' +
          '</div>' +
          '</div>' +
        '</div>';
      });
      container.innerHTML = html;
      container.querySelectorAll('.mem-card').forEach(function(el) {
        var sx = 0, sy = 0;
        el.addEventListener('touchstart', function(e) { sx = e.touches[0].clientX; sy = e.touches[0].clientY; }, {passive:true});
        el.addEventListener('touchmove', function(e) {
          var dx = e.touches[0].clientX - sx, dy = e.touches[0].clientY - sy;
          if (Math.abs(dx) > Math.abs(dy) && Math.abs(dx) > 5) {
            if (dx < -30) el.classList.add('swiped'); else if (dx > 10) el.classList.remove('swiped');
          }
        }, {passive:true});
        el.addEventListener('touchend', function() {
          if (el.classList.contains('swiped')) { clearTimeout(el._st); el._st = setTimeout(function() { el.classList.remove('swiped'); }, 1200); }
        });
        el.addEventListener('click', function(e) {
          if (el.classList.contains('swiped') && e.target.closest('.swipe-bg')) {
            e.stopPropagation();
            memAction('delete', el.dataset.id);
            el.classList.remove('swiped');
          }
        });
      });
    })
    .catch(function() { container.innerHTML = '<div class="empty-state">Failed to load.</div>'; });
}
