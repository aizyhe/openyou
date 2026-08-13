function openModal(id) {
  const modal = $('#' + id);
  if (modal) {
    modal.classList.add('show');
    const focusable = modal.querySelector('button, input, select, textarea, [tabindex]');
    if (focusable) setTimeout(() => focusable.focus(), 50);
  }
}

document.addEventListener('keydown', function(e) {
  if (e.key === 'Escape') {
    const open = document.querySelector('.modal-overlay.show');
    if (open) closeModal(open.id);
  }
});

function closeModal(id) {
  const modal = $('#' + id);
  if (modal) modal.classList.remove('show');
}

function closeAllModals() {
  $$('.modal-overlay').forEach(m => m.classList.remove('show'));
}

$$('.modal-overlay').forEach(modal => {
  modal.addEventListener('click', function(e) {
    if (e.target === this) closeModal(this.id);
  });
});

$$('[data-modal-close]').forEach(btn => {
  btn.addEventListener('click', () => {
    closeModal(btn.closest('.modal-overlay').id);
  });
});

$('#btn-rename-confirm').addEventListener('click', () => {
  const id = state._renameTargetId;
  const title = $('#rename-input').value.trim();
  if (id && title) {
    updateSessionTitle(id, title);
    renderSessionList();
    if (state.currentSessionId === id) {
      $('#chat-title').textContent = title;
    }
  }
  closeModal('modal-rename');
});

$('#btn-delete-confirm').addEventListener('click', () => {
  const id = state._pendingDeleteId;
  if (id) {
    deleteSession(id);
    if (state.currentSessionId === id) {
      state.currentSessionId = null;
      showPage('consult');
    }
    renderSessionList();
    showToast('Consult deleted');
  }
  closeModal('modal-delete');
});
