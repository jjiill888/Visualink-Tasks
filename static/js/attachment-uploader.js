// Attachment uploader: attaches to a root element with data-attach-uploader.
// Looks for:
//   input[type=file][data-attach-input]   — the picker
//   [data-attach-list]                      — container to render previews
//   [data-attach-ids]                       — hidden input accumulating ids (CSV)
// Also supports drag-drop and paste (from clipboard) on the root element.
(function(){
  var ACCEPT = 'image/jpeg,image/png,image/webp,image/heic,image/heif,.heic,.heif';

  function upload(root, file) {
    var list   = root.querySelector('[data-attach-list]');
    var idsEl  = root.querySelector('[data-attach-ids]');

    var item = document.createElement('div');
    item.className = 'attach-item attach-item--loading';
    item.innerHTML = '<div class="attach-spinner"></div>'
                   + '<div class="attach-name">' + (file.name || '图片') + '</div>';
    list.appendChild(item);

    var fd = new FormData();
    fd.append('file', file);
    fetch('/uploads/image', { method: 'POST', body: fd, credentials: 'same-origin' })
      .then(function(r){
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP '+r.status)); });
        return r.json();
      })
      .then(function(data){
        item.classList.remove('attach-item--loading');
        item.innerHTML = ''
          + '<img src="' + data.thumb_url + '" alt="" class="attach-thumb">'
          + '<button type="button" class="attach-remove" aria-label="移除">×</button>'
          + '<input type="hidden" data-attach-row value="' + data.id + '">';
        item.dataset.attachId = data.id;
        item.querySelector('.attach-remove').addEventListener('click', function(){
          item.remove();
          sync();
        });
        sync();
      })
      .catch(function(err){
        item.classList.remove('attach-item--loading');
        item.classList.add('attach-item--error');
        item.innerHTML = '<div class="attach-name attach-error">上传失败：' + (err.message || '未知错误') + '</div>'
                       + '<button type="button" class="attach-remove" aria-label="移除">×</button>';
        item.querySelector('.attach-remove').addEventListener('click', function(){
          item.remove();
        });
      });

    function sync() {
      var ids = [];
      list.querySelectorAll('[data-attach-row]').forEach(function(el){ ids.push(el.value); });
      idsEl.value = ids.join(',');
    }
  }

  function wire(root) {
    if (root.dataset.attachWired === '1') return;
    root.dataset.attachWired = '1';

    var picker = root.querySelector('[data-attach-input]');
    picker.setAttribute('accept', ACCEPT);
    picker.addEventListener('change', function(e){
      Array.prototype.forEach.call(e.target.files, function(f){ upload(root, f); });
      e.target.value = '';
    });

    root.addEventListener('dragover', function(e){
      if (e.dataTransfer && e.dataTransfer.types && Array.prototype.indexOf.call(e.dataTransfer.types, 'Files') >= 0) {
        e.preventDefault();
        root.classList.add('attach-dropping');
      }
    });
    root.addEventListener('dragleave', function(){ root.classList.remove('attach-dropping'); });
    root.addEventListener('drop', function(e){
      root.classList.remove('attach-dropping');
      if (!e.dataTransfer || !e.dataTransfer.files || !e.dataTransfer.files.length) return;
      e.preventDefault();
      Array.prototype.forEach.call(e.dataTransfer.files, function(f){
        if (/^image\//.test(f.type) || /\.(heic|heif)$/i.test(f.name)) upload(root, f);
      });
    });

    // Paste support: listen on the associated textarea if present.
    var pasteTarget = root.dataset.attachPaste
      ? document.querySelector(root.dataset.attachPaste)
      : null;
    if (pasteTarget) {
      pasteTarget.addEventListener('paste', function(e){
        if (!e.clipboardData || !e.clipboardData.items) return;
        Array.prototype.forEach.call(e.clipboardData.items, function(item){
          if (item.kind === 'file' && /^image\//.test(item.type)) {
            var f = item.getAsFile();
            if (f) upload(root, f);
          }
        });
      });
    }
  }

  function scan(scope) {
    (scope || document).querySelectorAll('[data-attach-uploader]').forEach(wire);
  }

  document.addEventListener('DOMContentLoaded', function(){ scan(document); });
  document.body.addEventListener('htmx:afterSwap', function(e){ scan(e.target); });
  window.wireAttachmentUploader = scan;

  // Clear all uploader previews and hidden ids under a scope (form/root element).
  // Called after successful comment submission so the next comment starts empty.
  window.clearAttachmentUploader = function(scope) {
    (scope || document).querySelectorAll('[data-attach-uploader]').forEach(function(root){
      var list  = root.querySelector('[data-attach-list]');
      var idsEl = root.querySelector('[data-attach-ids]');
      if (list)  list.innerHTML = '';
      if (idsEl) idsEl.value = '';
    });
  };

  // Lightbox: delegate click on any <a data-attach-lightbox>.
  document.addEventListener('click', function(e) {
    var a = e.target.closest ? e.target.closest('a[data-attach-lightbox]') : null;
    if (!a) return;
    e.preventDefault();
    var overlay = document.createElement('div');
    overlay.className = 'attach-lightbox';
    var img = document.createElement('img');
    img.src = a.getAttribute('href');
    img.alt = (a.querySelector('img') || {}).alt || '';
    overlay.appendChild(img);
    overlay.addEventListener('click', function(){ overlay.remove(); });
    function esc(ev){ if (ev.key === 'Escape'){ overlay.remove(); document.removeEventListener('keydown', esc); } }
    document.addEventListener('keydown', esc);
    document.body.appendChild(overlay);
  });
})();
