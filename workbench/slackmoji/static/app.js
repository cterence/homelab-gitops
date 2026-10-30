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
  var framesNote = document.getElementById('frames-note');
  var FRAMES_NOTE = 'Lower the size to keep more frames';

  // the note's line is always reserved; only its text comes and goes
  function note(show) {
    framesNote.textContent = show ? FRAMES_NOTE : '';
  }

  var AXES = ['frames', 'side'];
  var sliders = {
    frames: document.getElementById('frames-slider'),
    side: document.getElementById('size-slider')
  };
  var hints = {
    frames: document.getElementById('frames-out'),
    side: document.getElementById('size-out')
  };
  var ends = {
    frames: [document.getElementById('frames-min'), document.getElementById('frames-base')],
    side: [document.getElementById('size-min'), document.getElementById('size-base')]
  };

  var current = null;
  var currentFile = null;
  var busy = false;
  // sliders live only once a file has converted
  var controlsActive = false;
  // the axis the user dragged last: it is held at the dragged value,
  // and yields first when the pinned set cannot fit
  var priority = '';
  // the size is chosen first: frames drags hold it here
  var sizeHold = 0;
  // the true outcome side, which a stepped slider may not be able to show
  var sizeNow = 0;
  var framesInCount = 0;
  // false for still images: the frames dial has nothing to say
  var framesLive = false;
  // the most frames the locked size holds: the outcome of every
  // conversion that ran with frames free (upload, reset, shape, size
  // drag). Frames drags hold the size, so it cannot go stale.
  var framesMax = 0;
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

  // the dragged axis is pinned at its slider value; every other axis
  // sits at the optimum. The size slider is chosen first: frames
  // drags hold it through sizeHold. Colors are never pinned: they are
  // whatever budget is left, always maximized. A frames ask never
  // goes past what the locked size can hold.
  function pin(axis) {
    if (priority !== axis) return 0;

    var v = +sliders[axis].value;
    if (axis === 'frames') v = Math.min(v, framesMax);

    return v;
  }

  function updateLabels() {
    if (!report) {
      hints.frames.textContent = '';
      hints.side.textContent = '';
      return;
    }

    hints.frames.textContent = report.framesOut + ' of ' + report.framesIn;
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

    // the facts no slider carries: the byte budget and the palette the
    // leftover budget buys, always maximized
    stageMeta.textContent = fmtKB(rep.outputSize) + ' of 128 KB · ' +
      plural(rep.colors, 'color');

    // full ranges so any drag expresses a priority; the handle always
    // shows the outcome. The size steps by 16, so the top of the
    // slider must land on the grid.
    sliders.side.dataset.inPx = Math.max(rep.inputWidth, rep.inputHeight);

    // the slider spans the original frame count; the size caps the
    // ask (pin() clamps to framesMax)
    framesInCount = rep.framesIn;
    framesLive = rep.framesIn > 1;
    if (priority !== 'frames') framesMax = rep.framesOut;

    var fStep = Math.max(1, Math.round(rep.framesIn / 16));
    var fMin = 1 + ((rep.framesIn - 1) % fStep);

    sliders.frames.min = fMin;
    sliders.frames.max = rep.framesIn;
    sliders.frames.step = fStep;

    var sMin = Math.min(32, naturalMax());
    var sMax = naturalMax() - ((naturalMax() - sMin) % 16);

    sliders.side.min = sMin;
    sliders.side.max = sMax;

    ends.frames[0].textContent = sliders.frames.min;
    ends.frames[1].textContent = sliders.frames.max;
    ends.side[0].textContent = sMin + ' px';
    ends.side[1].textContent = sMax + ' px';

    sliders.frames.value = rep.framesOut;
    sliders.side.value = Math.max(rep.width, rep.height);
    sizeNow = Math.max(rep.width, rep.height);

    current = { dataUrl: rep.dataUrl, name: rep.name };
    downloadBtn.textContent = 'Download ' + (rep.dataUrl.indexOf('image/gif') !== -1 ? 'GIF' : 'PNG');

    report = rep;
    controlsActive = true;
    setControlsEnabled(true);
    updateLabels();
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
      // a still image has no frames to thin
      sliders[axis].disabled = !on || (axis === 'frames' && !framesLive);
    });

    document.querySelectorAll('input[name="mode"]').forEach(function (r) {
      r.disabled = !enabled;
    });

    resetBtn.disabled = !on;
    clearBtn.disabled = !enabled || !currentFile;
    downloadBtn.disabled = !enabled || !current;
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

    if (sizeHold) body.append('side', sizeHold);

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
    framesInCount = 0;
    framesMax = 0;
    framesLive = false;

    resetAdjustments();

    fileInput.value = '';
    preview.hidden = true;
    preview.removeAttribute('src');
    slotEmpty.hidden = false;
    slot.classList.remove('has-file');
    slot.setAttribute('aria-label', 'Choose a file');

    stageMeta.textContent = '';
    downloadBtn.textContent = 'Download';
    error.hidden = true;

    AXES.forEach(function (axis) {
      ends[axis][0].textContent = '';
      ends[axis][1].textContent = '';
      sliders[axis].value = sliders[axis].min;
    });

    setControlsEnabled(true);
    updateLabels();
  }

  function resetAdjustments() {
    priority = '';
    sizeHold = 0;
    note(false);
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
          ? v + ' of ' + framesInCount
          : v + ' px';
      }
    });

    sliders[axis].addEventListener('change', function () {
      if (!currentFile) return;

      priority = axis;

      // the size is chosen first: frames drags hold it at its true
      // outcome, which the stepped slider may not be able to show
      sizeHold = axis === 'side' ? 0 : sizeNow;

      if (axis === 'frames') {
        var raw = +sliders.frames.value;
        var ask = Math.min(raw, framesMax);

        // the size cannot hold more than the current state: settle
        // the handle and explain instead of converting to the same
        // result
        if (ask === report.framesOut) {
          note(raw > framesMax);
          sliders.frames.value = report.framesOut;
          updateLabels();
          return;
        }
      }

      // any release past the bound explains itself, whether the
      // conversion below it changes the state or not; every other
      // action clears the note
      note(axis === 'frames' && +sliders.frames.value > framesMax);

      convert(currentFile);
    });
  });

  document.querySelectorAll('input[name="mode"]').forEach(function (radio) {
    radio.addEventListener('change', function () {
      // a new shape is a fresh start: the optimum in that shape
      resetAdjustments();

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
