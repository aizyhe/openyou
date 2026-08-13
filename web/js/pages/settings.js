const SETTINGS_SCHEMA = [
  {
    id: 'ai-engine',
    title: 'AI Engine',
    rows: [
      { key: 'provider', label: 'Provider', type: 'select', options: [
        { value: 'deepseek', label: 'DeepSeek' },
        { value: 'openai', label: 'OpenAI' },
        { value: 'claude', label: 'Claude' },
      ]},
      { key: 'model', label: 'Model', type: 'text' },
      { key: 'apiKey', label: 'API Key', type: 'password', placeholder: 'sk-...' },
      { key: 'baseUrl', label: 'Base URL', type: 'text' },
      { key: 'maxTokens', label: 'Max Tokens', type: 'number', min: 256, max: 128000 },
      { key: 'temperature', label: 'Temperature', type: 'range', min: 0, max: 2, step: 0.1 },
    ]
  },
  {
    id: 'data',
    title: 'Backup',
    rows: [
      { key: 'data--backup', label: 'Export', type: 'button', btnText: 'Download', action: 'backup' },
      { key: 'data--restore', label: 'Import', type: 'button', btnText: 'Choose File', action: 'restore' },
      // backupTime needs a real row: updateSettingsDisplay() looks it up by data-key.
      { key: 'backupTime', label: 'Last Backup', type: 'info', value: 'Never' },
    ]
  },
  {
    id: 'diagnostics',
    title: 'Diagnostics',
    rows: [
      { key: 'diag--status', label: 'Status', type: 'info', value: 'Checking...' },
      { key: 'diag--latency', label: 'LLM Latency', type: 'info', value: '--' },
      { key: 'diag--sessions', label: 'Sessions', type: 'info', value: '--' },
      { key: 'diag--memories', label: 'Memories', type: 'info', value: '--' },
      { key: 'diag--people', label: 'People', type: 'info', value: '--' },
      { key: 'diag--test', label: 'Test Connection', type: 'button', action: 'test-connection', btnText: 'Test' },
    ]
  },
  {
    id: 'about',
    title: 'About',
    rows: [
      { key: 'version', label: 'Version', type: 'info', value: 'v' + APP_VERSION },
      { key: 'build', label: 'Build', type: 'info', value: BUILD_DATE },
      { key: 'author', label: 'Author', type: 'info', value: 'hezhiyong' },
    ]
  },
];

function renderSettings() {
  const container = $('#settings-scroll');
  let html = '';
  SETTINGS_SCHEMA.forEach(section => {
    html += `<div class="settings-section" data-section="${section.id}">
      <div class="settings-section-header">
        <span class="section-title">${section.title}</span>
      </div>
      <div class="settings-section-body"><div class="settings-section-inner">`;

    section.rows.forEach(row => {
      const val = state.settings[row.key] ?? '';
      html += `<div class="setting-row" data-key="${row.key}">
        <label>${row.label}</label>`;

      switch (row.type) {
        case 'text':
          html += `<input type="text" value="${escHtml(String(val))}" placeholder="${row.placeholder || ''}">`;
          break;
        case 'password':
          // daemon key stays editable so the user can override it; blank keeps the existing key (empty is never sent).
          const ph = row.key === 'apiKey' && state.daemonHasKey
            ? '已配置 — 留空保持不变'
            : (row.placeholder || '');
          html += `<div style="display:flex;align-items:center;gap:4px">` +
            `<input type="password" value="${escHtml(String(val))}" placeholder="${ph}" style="flex:1">` +
            `<button type="button" class="btn-small pw-toggle" style="padding:2px 6px;font-size:var(--fs-sm)" data-visible="0">👁</button>` +
            `</div>`;
          break;
        case 'number':
          // escHtml: restored-backup values with an unescaped quote break out of the attribute (stored XSS).
          html += `<input type="number" value="${escHtml(String(val))}" min="${row.min || 0}" max="${row.max || 999999}">`;
          break;
        case 'range':
          html += `<input type="range" value="${escHtml(String(val))}" min="${row.min || 0}" max="${row.max || 2}" step="${row.step || 0.1}">
            <span style="margin-left:8px;font-size:var(--fs-md);color:var(--text-dim);min-width:32px;text-align:right">${escHtml(String(val))}</span>`;
          break;
        case 'select':
          const opts = row.options || [];
          if (opts.length === 2 && !row.forceSelect) {
            html += `<div class="segmented-control" data-key="${row.key}">`;
            opts.forEach(o => {
              html += `<button class="seg-option${val == o.value ? ' active' : ''}" data-value="${o.value}">${o.label}</button>`;
            });
            html += `</div>`;
          } else {
            html += `<select>${opts.map(o => `<option value="${o.value}" ${val == o.value ? 'selected' : ''}>${o.label}</option>`).join('')}</select>`;
          }
          break;
        case 'checkbox':
          html += `<input type="checkbox" ${val ? 'checked' : ''}>`;
          break;
        case 'info':
          let infoVal = row.value || '';
          if (row.key === 'backupTime') infoVal = state.settings.backupTime ? formatTime(state.settings.backupTime) : 'Never';
          html += `<span class="setting-disabled">${infoVal}</span>`;
          break;
        case 'button':
          html += `<button class="btn btn-cancel" style="font-size:var(--fs-md);padding:4px 12px;" data-action="${row.action}">${row.btnText || row.label || row.action}</button>`;
          break;
      }

      if (row.hint) html += `<div class="settings-hint">${row.hint}</div>`;
      html += `</div>`;
    });

    html += `</div></div></div>`;
  });

  container.innerHTML = html;
  bindSettingsEvents(container);
}

function bindSettingsEvents(container) {
  container.querySelectorAll('.settings-section-header').forEach(header => {
    header.addEventListener('click', () => {
      header.parentElement.classList.toggle('open');
      saveSettingsOpenState();
    });
  });

  loadSettingsOpenState(container);

  container.querySelectorAll('.setting-row input[type="text"], .setting-row input[type="number"], .setting-row input[type="password"]').forEach(input => {
    input.addEventListener('change', () => {
      const key = input.closest('.setting-row').dataset.key;
      if (key && key.indexOf('--placeholder') < 0) {
        state.settings[key] = input.type === 'number' ? (isNaN(Number(input.value)) ? 0 : Number(input.value)) : input.value;
        saveSettings();
      }
    });
  });

  container.querySelectorAll('.setting-row select').forEach(select => {
    select.addEventListener('change', () => {
      const key = select.closest('.setting-row').dataset.key;
      if (key) {
        state.settings[key] = select.options[select.selectedIndex].value;
        saveSettings();
      }
    });
  });

  container.querySelectorAll('.setting-row input[type="checkbox"]').forEach(cb => {
    cb.addEventListener('change', () => {
      const key = cb.closest('.setting-row').dataset.key;
      if (key) {
        state.settings[key] = cb.checked;
        saveSettings();
      }
    });
  });

  container.querySelectorAll('.setting-row input[type="range"]').forEach(range => {
    const debouncedSave = debounce(() => {
      const key = range.closest('.setting-row').dataset.key;
      if (key) {
        state.settings[key] = parseFloat(range.value);
        saveSettings();
      }
    }, 500);
    range.addEventListener('input', () => {
      const row = range.closest('.setting-row');
      const valSpan = row.querySelector('span');
      if (valSpan) valSpan.textContent = range.value;
      debouncedSave();
    });
  });

  container.querySelectorAll('.pw-toggle').forEach(btn => {
    btn.addEventListener('click', () => {
      const inp = btn.previousElementSibling;
      if (!inp) return;
      const showing = btn.dataset.visible === '1';
      inp.type = showing ? 'password' : 'text';
      btn.dataset.visible = showing ? '0' : '1';
      btn.textContent = showing ? '👁' : '🙈';
    });
  });

  container.querySelectorAll('button[data-action]').forEach(btn => {
    btn.addEventListener('click', () => {
      const action = btn.dataset.action;
      if (action === 'backup') {
        // daemon can't see WebView localStorage, so settings ride along; api_key deliberately excluded.
        let starHistory = [];
        try { starHistory = JSON.parse(localStorage.getItem('oy_star_history_v1') || '[]'); } catch (e) {}
        const settings = { ...state.settings };
        delete settings.apiKey;
        const extras = JSON.stringify({ star_history: starHistory, settings: settings });
        const fname = 'openyou-backup-' + _localStamp() + '.zip';
        const bridge = window.openCodeBridge;
        if (bridge && bridge.exportBackup) {
          // Kotlin streams to MediaStore, avoiding whole-package base64 (large backups would OOM).
          showToast('Exporting…');
          let res = null;
          try { res = JSON.parse(bridge.exportBackup(fname, extras)); } catch (e) {}
          if (!res || res.ok !== true) {
            showToast('Export failed' + (res && res.error ? ': ' + res.error : ''), 'error');
            return;
          }
          const warns = parseInt(res.warnings || '0', 10);
          showToast(warns > 0 ? 'Saved (INCOMPLETE: ' + warns + ' item(s) failed)' : 'Saved to Download/H/' + res.name, warns > 0 ? 'warn' : '');
          state.settings.backupTime = ts();
          saveSettings();
          updateSettingsDisplay('backupTime', formatTime(state.settings.backupTime));
          return;
        }
        // desktop fallback without the bridge: fetch the zip to trigger a download.
        fetch('/api/backup/export', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: extras
        })
          .then(r => {
            if (!r.ok) throw new Error('HTTP ' + r.status);
            // a partially-failed backup still ships but is flagged, so users don't mistake it for a complete one.
            const warns = parseInt(r.headers.get('X-Backup-Warnings') || '0', 10);
            // must await arrayBuffer() first, else Uint8Array(Promise) yields a 0-length empty zip.
            return r.arrayBuffer().then(buf => ({ buf: buf, warns: warns }));
          })
          .then(({ buf, warns }) => {
            const blob = new Blob([buf], { type: 'application/zip' });
            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = fname;
            a.click();
            setTimeout(() => URL.revokeObjectURL(url), 1000);
            showToast(warns > 0 ? 'Backup exported (INCOMPLETE: ' + warns + ' item(s) failed)' : 'Backup exported', warns > 0 ? 'warn' : '');
            state.settings.backupTime = ts();
            saveSettings();
            updateSettingsDisplay('backupTime', formatTime(state.settings.backupTime));
          })
          .catch(e => showToast('Export failed: ' + e.message, 'error'));
      } else if (action === 'restore') {
        const input = document.createElement('input');
        input.type = 'file';
        input.accept = '.zip,application/zip';
        input.style.display = 'none';
        // ArkWeb needs the input attached to open the picker; replace the stale one a cancel left.
        const prev = document.getElementById('restore-file-input');
        if (prev) prev.remove();
        input.id = 'restore-file-input';
        document.body.appendChild(input);
        input.onchange = async () => {
          const file = input.files[0];
          input.remove();
          if (!file) return;
          // only accept our own export filenames; reject the rest before they reach the daemon.
          if (!/^openyou-backup-\d{8}-\d{6}\.zip$/i.test(file.name)) {
            showToast('Invalid name — expected openyou-backup-yyyymmdd-hhmmss.zip', 'error');
            return;
          }
          // import is add-only — fills gaps, never deletes; still worth confirming.
          if (!confirm('Import adds missing data from this backup. Existing data is kept (nothing is deleted or overwritten). Continue?')) return;

          const bridge = window.openCodeBridge;
          if (bridge && bridge.beginImport) {
            // chunked stream upload avoids a whole-package base64 OOM in JS/JNI.
            showToast('Importing…');
            try {
              if (!bridge.beginImport()) throw new Error('cannot start import');
              const CHUNK = 3 * 1024 * 1024;
              for (let off = 0; off < file.size; off += CHUNK) {
                const buf = await file.slice(off, Math.min(off + CHUNK, file.size)).arrayBuffer();
                if (!bridge.appendImportChunk(_bufToBase64(buf))) throw new Error('write failed');
              }
              const respText = bridge.finishImport();
              let d = null;
              try { d = JSON.parse(respText); } catch (e) {}
              if (!d) throw new Error('bad daemon response');
              handleImportResult(d);
            } catch (e) {
              showToast('Import failed: ' + e.message, 'error');
            }
            return;
          }

          // desktop fallback: whole-package base64 envelope upload.
          const reader = new FileReader();
          reader.onload = () => {
            try {
              const body = JSON.stringify({ format: 'zip', name: file.name, data: _bufToBase64(reader.result) });
              fetch('/api/backup/import', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: body
              }).then(r => r.json())
                .then(handleImportResult)
                .catch(e => showToast('Import failed: ' + e.message, 'error'));
            } catch (e) {
              showToast('Invalid backup file', 'error');
            }
          };
          reader.readAsArrayBuffer(file);
        };
        input.click();
      } else if (action === 'test-connection') {
        testConnection();
      }
    });
  });
}

// Whitelist restored keys against defaultSettings() — a crafted manifest can't inject __proto__ etc.
function handleImportResult(d) {
  // ok can be the string "false" (truthy) — compare explicitly, else a 207 partial failure reads as success.
  const okFlag = d.ok === 'true' || d.ok === true;
  if (!okFlag && d.error) {
    showToast('Restore failed: ' + d.error, 'warn');
    return;
  }
  if (Array.isArray(d.star_history)) {
    try { localStorage.setItem('oy_star_history_v1', JSON.stringify(d.star_history)); } catch (e) {}
  }
  if (d.settings && typeof d.settings === 'object') {
    const known = defaultSettings();
    Object.keys(d.settings).forEach(k => {
      if (k === 'apiKey' || k === 'backupTime') return;
      if (!Object.prototype.hasOwnProperty.call(known, k)) return;
      state.settings[k] = d.settings[k];
    });
    saveSettings();
  }
  state.settings.backupTime = ts();
  saveSettings();
  updateSettingsDisplay('backupTime', formatTime(state.settings.backupTime));
  if (!okFlag) {
    showToast('Restore partial: ' + ((d.failures || []).length) + ' item(s) failed', 'warn');
  } else {
    const parts = [];
    if (d.files_restored) parts.push(d.files_restored + ' files');
    if (d.sessions_restored) parts.push(d.sessions_restored + ' sessions');
    if (d.memories_restored) parts.push(d.memories_restored + ' memories');
    if (d.tasks_restored) parts.push(d.tasks_restored + ' tasks');
    if (d.feedback_restored) parts.push(d.feedback_restored + ' feedback');
    showToast(parts.length ? 'Restored ' + parts.join(', ') : 'Backup imported (already up to date)');
  }
  loadHome();
  loadContextMemories && loadContextMemories();
  renderSessionList && renderSessionList();
  loadTasks && loadTasks();
  loadContextInsides && loadContextInsides();
  loadContextRules && loadContextRules();
  loadContextSkills && loadContextSkills();
}

// name by local time; toISOString() is UTC and would backdate a UTC+8 morning backup by a day.
function _localStamp() {
  const d = new Date();
  const pad = n => String(n).padStart(2, '0');
  return d.getFullYear() + pad(d.getMonth() + 1) + pad(d.getDate()) + '-' +
    pad(d.getHours()) + pad(d.getMinutes()) + pad(d.getSeconds());
}

// chunked to avoid apply() exceeding the call-stack argument limit.
function _bufToBase64(buf) {
  const bytes = new Uint8Array(buf);
  let binary = '';
  for (let i = 0; i < bytes.length; i += 0x8000) {
    binary += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
  }
  return btoa(binary);
}

function saveSettingsOpenState() {
  const open = [];
  $$('.settings-section.open').forEach(s => open.push(s.dataset.section));
  try { localStorage.setItem('oy_settings_open', JSON.stringify(open)); } catch(e) {}
}

function loadSettingsOpenState(container) {
  try {
     const open = JSON.parse(localStorage.getItem('oy_settings_open') || '[]');
    if (open.length === 0) {
      return;
    }
    open.forEach(id => {
      const section = container.querySelector(`.settings-section[data-section="${id}"]`);
      if (section) section.classList.add('open');
    });
  } catch(e) {}
}
