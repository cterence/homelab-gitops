(function () {
  var fileInput = document.getElementById('file');
  var fileName = document.getElementById('file-name');
  var drop = document.getElementById('drop');
  var downloadBtn = document.getElementById('download-btn');
  var previewCol = document.getElementById('preview-col');
  var preview = document.getElementById('preview');
  var finding = document.getElementById('finding');
  var error = document.getElementById('error');
  var resultMeta = document.getElementById('result-meta');
  var framesOut = document.getElementById('frames-out');

  var fields = {
    size: document.getElementById('f-size'),
    dims: document.getElementById('f-dims'),
    frames: document.getElementById('f-frames'),
    colors: document.getElementById('f-colors'),
    input: document.getElementById('f-input')
  };

  var current = null;
  var currentFile = null;
  var busy = false;
  var framesIn = 0;
  var requestedFrames = 0;

  function fmtKB(bytes) {
    return (bytes / 1024).toFixed(1).replace(/\.0$/, '') + ' KB';
  }

  function plural(n, noun) {
    return n + ' ' + noun + (n === 1 ? '' : 's');
  }

  function mode() {
    return document.querySelector('input[name="mode"]:checked').value;
  }

  function preset(name) {
    return +document.querySelector('input[name="' + name + '"]:checked').value;
  }

  // framesIn 0 means no animated file yet; the frame presets stay disabled.
  function frameCap() {
    var pct = preset('framesPct');

    if (framesIn < 2 || pct >= 100) return 0;
    return Math.max(1, Math.round(framesIn * pct / 100));
  }

  function updateLabels() {
    if (framesIn < 2) {
      framesOut.textContent = 'all';
    } else {
      framesOut.textContent = 'all ' + framesIn;
    }

    if (frameCap() > 0) framesOut.textContent = frameCap() + ' frames';
  }

  function showResult(report, dataUrl, name) {
    error.hidden = true;
    previewCol.hidden = false;

    preview.src = dataUrl;
    preview.alt = 'Converted preview, ' + report.width + ' by ' + report.height + ' pixels';
    preview.hidden = false;

    if (report.framesIn > 1) {
      framesIn = report.framesIn;

      document.querySelectorAll('input[name="framesPct"]').forEach(function (r) {
        r.disabled = false;
      });
    }

    updateLabels();

    var sizeNote = '';

    if (!report.passthrough && Math.max(report.width, report.height) < Math.min(preset('maxSide'), 128)) {
      sizeNote = ' at ' + Math.max(report.width, report.height) + ' px';
    }

    var framesText;
    if (report.passthrough) {
      finding.textContent = 'Already within the limits, returned unchanged.';
    } else if (report.framesOut === report.framesIn) {
      framesText = 'all ' + plural(report.framesIn, 'frame') + ' kept';
      finding.textContent = 'Fits the limits: ' + fmtKB(report.outputSize) + ' of 128 KB, ' + framesText + sizeNote + '.';
    } else if (report.framesOut === requestedFrames) {
      framesText = report.framesOut + ' of ' + report.framesIn + ' frames kept';
      finding.textContent = 'Fits the limits: ' + fmtKB(report.outputSize) + ' of 128 KB, ' + framesText +
        ' at your frame cap' + sizeNote + '.';
    } else {
      framesText = report.framesOut + ' of ' + report.framesIn + ' frames kept';
      finding.innerHTML = 'Fits the limits: ' + fmtKB(report.outputSize) +
        ' of 128 KB, <span class="dropped">' + framesText + '</span> after quality was exhausted' + sizeNote + '.';
    }

    resultMeta.textContent = report.passthrough ? 'unchanged' : 'converted';

    fields.size.textContent = fmtKB(report.outputSize) + ' of 128 KB';
    fields.dims.textContent = report.width + ' × ' + report.height + ' px';
    fields.frames.textContent = report.framesOut + ' of ' + report.framesIn;
    fields.colors.textContent = plural(report.colors, 'color');
    fields.input.textContent = fmtKB(report.inputSize);

    current = { dataUrl: dataUrl, name: name };
    downloadBtn.hidden = false;
    downloadBtn.textContent = 'Download ' + (dataUrl.indexOf('image/gif') !== -1 ? 'GIF' : 'PNG');
  }

  function showError(message) {
    error.textContent = message;
    error.hidden = false;
  }

  function convert(file) {
    if (!file || busy) return;

    busy = true;
    error.hidden = true;

    requestedFrames = frameCap();

    var body = new FormData();
    body.append('file', file);
    body.append('mode', mode());

    if (requestedFrames > 0) body.append('maxFrames', requestedFrames);

    if (preset('maxSide') < 128) body.append('maxSide', preset('maxSide'));

    fetch('/convert', { method: 'POST', body: body })
      .then(function (r) {
        return r.json().then(function (j) { return { ok: r.ok, j: j }; });
      })
      .then(function (res) {
        if (!res.ok) throw new Error(res.j.error || 'Conversion failed');
        showResult(res.j.report, res.j.data, file.name);
      })
      .catch(function (err) {
        showError(err.message);
      })
      .finally(function () {
        busy = false;
      });
  }

  function onFile(file) {
    if (!file) return;

    currentFile = file;
    fileName.textContent = file.name + ', ' + fmtKB(file.size);
    fileName.hidden = false;
    convert(file);
  }

  fileInput.addEventListener('change', function () {
    onFile(fileInput.files[0]);
  });

  document.querySelectorAll('input[name="mode"], input[name="framesPct"], input[name="maxSide"]').forEach(function (radio) {
    radio.addEventListener('change', function () {
      updateLabels();

      if (currentFile) convert(currentFile);
    });
  });

  updateLabels();

  downloadBtn.addEventListener('click', function () {
    if (!current) return;

    var a = document.createElement('a');
    a.href = current.dataUrl;
    a.download = current.name.replace(/\.[^.]+$/, '') + '-slackmoji' +
      (current.dataUrl.indexOf('image/gif') !== -1 ? '.gif' : '.png');
    a.click();
  });

  ['dragenter', 'dragover'].forEach(function (evt) {
    drop.addEventListener(evt, function (e) {
      e.preventDefault();
      drop.classList.add('over');
    });
  });

  ['dragleave', 'drop'].forEach(function (evt) {
    drop.addEventListener(evt, function (e) {
      e.preventDefault();
      drop.classList.remove('over');
    });
  });

  drop.addEventListener('drop', function (e) {
    var file = e.dataTransfer.files[0];

    if (file) {
      var dt = new DataTransfer();
      dt.items.add(file);
      fileInput.files = dt.files;
    }

    onFile(file);
  });
})();
