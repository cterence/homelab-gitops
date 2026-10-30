(function () {
  var fileInput = document.getElementById('file');
  var slot = document.getElementById('slot');
  var slotEmpty = document.getElementById('slot-empty');
  var preview = document.getElementById('preview');
  var clearBtn = document.getElementById('clear-btn');
  var downloadBtn = document.getElementById('download-btn');
  var stageMeta = document.getElementById('stage-meta');
  var spinner = document.getElementById('spinner');
  var error = document.getElementById('error');
  var resetBtn = document.getElementById('reset-btn');

  var AXES = ['frames', 'colors', 'side'];
  var sliders = {
    frames: document.getElementById('frames-slider'),
    colors: document.getElementById('colors-slider'),
    side: document.getElementById('size-slider')
  };
  var hints = {
    frames: document.getElementById('frames-out'),
    colors: document.getElementById('colors-out'),
    side: document.getElementById('size-out')
  };
  var locks = {
    frames: document.getElementById('lock-frames'),
    colors: document.getElementById('lock-colors'),
    side: document.getElementById('lock-size')
  };
  var ends = {
    frames: [document.getElementById('frames-min'), document.getElementById('frames-base')],
    colors: [document.getElementById('colors-min'), document.getElementById('colors-base')],
    side: [document.getElementById('size-min'), document.getElementById('size-base')]
  };

  var current = null;
  var currentFile = null;
  var busy = false;
  // sliders live only once a file has converted
  var controlsActive = false;
  // the axis the user dragged last: its pin yields first when the
  // pinned set cannot fit, so a drag is a request, never an error
  var priority = '';
  var report = null;

  function fmtKB(bytes) {
    return (bytes / 1024).toFixed(1).replace(/\.0$/, '') + ' KB';
  }

  function plural(n, noun) {
    return n + ' ' + noun + (n === 1 ? '' : 's');
  }

  function mode() {
    return document.querySelector('input[name="mode"]:checked').value;
  }

  function naturalMax() {
    if (mode() === 'square') return 128;
    return Math.min(+sliders.side.dataset.inPx || 128, 128);
  }

  // a locked or priority axis is pinned exactly; the free axes degrade
  // to pay for it. A slider left at its maximum is a free axis.
  function pin(axis) {
    if (locks[axis].checked || priority === axis) return +sliders[axis].value;
    return 0;
  }

  function updateLabels() {
    if (!report) {
      hints.frames.textContent = 'all';
      hints.colors.textContent = 'best';
      hints.side.textContent = '128 px max';
      return;
    }

    hints.frames.textContent = report.framesOut + ' of ' + report.framesIn;
    hints.colors.textContent = plural(report.colors, 'color');
    hints.side.textContent = Math.max(report.width, report.height) + ' px';
  }

  function showResult(rep) {
    error.hidden = true;

    preview.src = rep.dataUrl;
    preview.alt = 'Converted preview, ' + rep.width + ' by ' + rep.height + ' pixels';
    preview.hidden = false;
    slotEmpty.hidden = true;
    slot.classList.add('has-file');
    slot.setAttribute('aria-label', 'Replace image');

    // the one fact no slider carries: the byte budget
    stageMeta.textContent = fmtKB(rep.outputSize) + ' of 128 KB';
    stageMeta.hidden = false;

    // full ranges so any drag expresses a priority; the handle always
    // shows the outcome
    sliders.side.dataset.inPx = Math.max(rep.inputWidth, rep.inputHeight);

    sliders.frames.min = 1;
    sliders.frames.max = rep.framesIn;

    sliders.colors.min = Math.min(16, rep.colors);
    sliders.colors.max = 255;

    sliders.side.min = Math.min(32, naturalMax());
    sliders.side.max = naturalMax();

    ends.frames[0].textContent = sliders.frames.min;
    ends.frames[1].textContent = sliders.frames.max;
    ends.colors[0].textContent = sliders.colors.min;
    ends.colors[1].textContent = sliders.colors.max;
    ends.side[0].textContent = sliders.side.min + ' px';
    ends.side[1].textContent = sliders.side.max + ' px';

    sliders.frames.value = rep.framesOut;
    sliders.colors.value = rep.colors;
    sliders.side.value = Math.max(rep.width, rep.height);

    report = rep;
    controlsActive = true;
    setControlsEnabled(true);
    updateLabels();

    current = { dataUrl: rep.dataUrl, name: rep.name };
    downloadBtn.hidden = false;
    downloadBtn.textContent = 'Download ' + (rep.dataUrl.indexOf('image/gif') !== -1 ? 'GIF' : 'PNG');
    clearBtn.hidden = false;
  }

  function showError(message) {
    error.textContent = message;
    error.hidden = false;
  }

  // the fixed budget for one conversion, including the ladder searches
  var CONVERT_TIMEOUT_MS = 20000;

  function setControlsEnabled(enabled) {
    var on = enabled && controlsActive;

    AXES.forEach(function (axis) {
      // a locked slider cannot be dragged: its value moves for nothing
      sliders[axis].disabled = !on || locks[axis].checked;
      locks[axis].disabled = !on;
    });

    document.querySelectorAll('input[name="mode"]').forEach(function (r) {
      r.disabled = !enabled;
    });

    resetBtn.disabled = !on;
    clearBtn.disabled = !enabled;
    downloadBtn.disabled = !enabled;
    slot.classList.toggle('busy', !enabled);

    spinner.hidden = enabled;
  }

  function convert(file) {
    if (!file || busy) return;

    busy = true;
    error.hidden = true;
    setControlsEnabled(false);

    var body = new FormData();
    body.append('file', file);
    body.append('mode', mode());

    AXES.forEach(function (axis) {
      var p = pin(axis);
      if (p) body.append(axis, p);
    });

    if (priority) body.append('priority', priority);

    var ctrl = new AbortController();

    var timer = setTimeout(function () {
      ctrl.abort();
    }, CONVERT_TIMEOUT_MS);

    fetch('/convert', { method: 'POST', body: body, signal: ctrl.signal })
      .then(function (r) {
        return r.json().then(function (j) { return { ok: r.ok, j: j }; });
      })
      .then(function (res) {
        if (!res.ok) throw new Error(res.j.error || 'Conversion failed');
        showResult(Object.assign({ dataUrl: res.j.data, name: file.name }, res.j.report));
      })
      .catch(function (err) {
        showError(err.name === 'AbortError'
          ? 'Timed out: no optimum found within ' + (CONVERT_TIMEOUT_MS / 1000) + ' s'
          : err.message);
      })
      .finally(function () {
        clearTimeout(timer);
        busy = false;
        setControlsEnabled(true);
      });
  }

  function onFile(file) {
    if (!file) return;

    currentFile = file;
    slotEmpty.hidden = true;
    resetBtn.hidden = false;

    resetAdjustments();
    convert(file);
  }

  // back to the empty dropzone: no file, no preview, sliders asleep
  function clearFile() {
    if (busy) return;

    currentFile = null;
    current = null;
    report = null;
    controlsActive = false;
    priority = '';

    resetAdjustments();

    fileInput.value = '';
    preview.hidden = true;
    preview.removeAttribute('src');
    slotEmpty.hidden = false;
    slot.classList.remove('has-file');
    slot.setAttribute('aria-label', 'Choose a file');

    stageMeta.hidden = true;
    downloadBtn.hidden = true;
    clearBtn.hidden = true;
    resetBtn.hidden = true;
    error.hidden = true;

    setControlsEnabled(true);
    updateLabels();
  }

  function resetAdjustments() {
    priority = '';
    AXES.forEach(function (axis) {
      locks[axis].checked = false;
    });
  }

  fileInput.addEventListener('change', function () {
    onFile(fileInput.files[0]);
  });

  // the filled slot is a replace button: click or Enter picks a new file
  slot.addEventListener('click', function () {
    if (currentFile && !busy) fileInput.click();
  });

  slot.addEventListener('keydown', function (e) {
    if (currentFile && !busy && (e.key === 'Enter' || e.key === ' ')) {
      e.preventDefault();
      fileInput.click();
    }
  });

  clearBtn.addEventListener('click', clearFile);

  AXES.forEach(function (axis) {
    // dragging shows the live request; releasing makes it the priority
    sliders[axis].addEventListener('input', function () {
      if (report) {
        var v = +sliders[axis].value;
        hints[axis].textContent = axis === 'frames'
          ? v + ' of ' + sliders.frames.max
          : axis === 'colors' ? plural(v, 'color') : v + ' px';
      }
    });

    sliders[axis].addEventListener('change', function () {
      if (!currentFile || locks[axis].checked) return;
      priority = axis;
      convert(currentFile);
    });

    // locking freezes the value; no conversion runs for a lock alone
    locks[axis].addEventListener('change', function () {
      setControlsEnabled(true);
    });
  });

  document.querySelectorAll('input[name="mode"]').forEach(function (radio) {
    radio.addEventListener('change', function () {
      if (currentFile) convert(currentFile);
    });
  });

  updateLabels();

  resetBtn.addEventListener('click', function () {
    if (!currentFile) return;

    resetAdjustments();
    convert(currentFile);
  });

  downloadBtn.addEventListener('click', function () {
    if (!current) return;

    var a = document.createElement('a');
    a.href = current.dataUrl;
    a.download = current.name.replace(/\.[^.]+$/, '') + '-slackmoji' +
      (current.dataUrl.indexOf('image/gif') !== -1 ? '.gif' : '.png');
    a.click();
  });

  ['dragenter', 'dragover'].forEach(function (evt) {
    slot.addEventListener(evt, function (e) {
      e.preventDefault();
      slot.classList.add('over');
    });
  });

  ['dragleave', 'drop'].forEach(function (evt) {
    slot.addEventListener(evt, function (e) {
      e.preventDefault();
      slot.classList.remove('over');
    });
  });

  slot.addEventListener('drop', function (e) {
    var file = e.dataTransfer.files[0];

    if (file) {
      var dt = new DataTransfer();
      dt.items.add(file);
      fileInput.files = dt.files;
    }

    onFile(file);
  });
})();
