(function () {
  var fileInput = document.getElementById('file');
  var fileName = document.getElementById('file-name');
  var drop = document.getElementById('drop');
  var downloadBtn = document.getElementById('download-btn');
  var previewCol = document.getElementById('preview-col');
  var preview = document.getElementById('preview');
  var error = document.getElementById('error');
  var resultMeta = document.getElementById('result-meta');
  var framesHint = document.getElementById('frames-out');
  var colorsHint = document.getElementById('colors-out');
  var sizeHint = document.getElementById('size-out');
  var resetBtn = document.getElementById('reset-btn');

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
  var pending = false;
  var framesIn = 0;
  var inPx = 0;
  var bestColors = 0;

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
    return document.querySelector('input[name="' + name + '"]:checked').value;
  }

  function naturalMax() {
    if (mode() === 'square') return 128;
    return Math.min(inPx || 128, 128);
  }

  // relative caps, resolved against the current file
  function frameCap() {
    var pct = +preset('framesPct');

    if (framesIn < 2 || pct >= 100) return 0;
    return Math.max(1, Math.round(framesIn * pct / 100));
  }

  function sizeCap() {
    var pct = +preset('sizePct');

    if (pct >= 100) return 0;
    return Math.min(128, Math.max(32, Math.round(naturalMax() * pct / 100)));
  }

  function colorsCap() {
    var v = preset('colorsPct');

    if (v === '100') return 0;
    if (v === 'floor') return 16;
    return Math.max(16, Math.round(bestColors * (+v) / 100));
  }

  function updateLabels() {
    if (framesIn < 2 || frameCap() === 0) {
      framesHint.textContent = framesIn < 2 ? 'all' : 'all ' + framesIn;
    } else {
      framesHint.textContent = frameCap() + ' frames';
    }

    var sc = sizeCap();
    sizeHint.textContent = (sc || naturalMax()) + ' px';

    var cc = colorsCap();
    colorsHint.textContent = cc === 0 ? (bestColors ? plural(bestColors, 'color') : 'best') : cc + ' colors';
  }

  function showResult(report, dataUrl, name, request) {
    error.hidden = true;
    previewCol.hidden = false;

    preview.src = dataUrl;
    preview.alt = 'Converted preview, ' + report.width + ' by ' + report.height + ' pixels';
    preview.hidden = false;

    if (report.framesIn > 1) {
      framesIn = report.framesIn;
    }

    inPx = Math.max(report.inputWidth, report.inputHeight);

    // the anchor for the relative color scale is the uncapped proposal
    if (!report.passthrough && request.colors === '100') {
      bestColors = report.colors;
    }

    resultMeta.textContent = report.passthrough ? 'unchanged' : 'converted';

    var px = Math.max(report.width, report.height);

    fields.size.textContent = fmtKB(report.outputSize) + ' of 128 KB';
    fields.dims.textContent = report.width + ' × ' + report.height + ' px';
    fields.frames.textContent = report.framesOut + ' of ' + report.framesIn;
    fields.colors.textContent = plural(report.colors, 'color');
    fields.input.textContent = fmtKB(report.inputSize);

    // red marks what was sacrificed relative to the input
    fields.dims.classList.toggle('dropped', !report.passthrough && px < inPx);
    fields.frames.classList.toggle('dropped', report.framesOut < report.framesIn);

    // a control the user has not touched shows the algorithm's decision;
    // a control the user set stays set
    if (!report.passthrough) {
      syncControls(px, report.framesOut, report.framesIn, request);
    }

    updateLabels();

    current = { dataUrl: dataUrl, name: name };
    downloadBtn.hidden = false;
    downloadBtn.textContent = 'Download ' + (dataUrl.indexOf('image/gif') !== -1 ? 'GIF' : 'PNG');
  }

  function nearestPreset(name, value, min) {
    if (value < min) return;

    var nearest = null;
    var bestDist = 1e9;

    document.querySelectorAll('input[name="' + name + '"]').forEach(function (r) {
      var d = Math.abs(+r.value - value);

      if (d < bestDist) {
        bestDist = d;
        nearest = +r.value;
      }
    });

    document.querySelectorAll('input[name="' + name + '"]').forEach(function (r) {
      r.checked = +r.value === nearest;
    });
  }

  function syncControls(px, framesOut, framesIn, request) {
    // snap size to the smallest preset whose cap still covers the
    // outcome, so the next conversion can always reach the same point
    if (request.size >= 100) {
      var steps = [25, 50, 75, 100];

      for (var i = 0; i < steps.length; i++) {
        var cap = steps[i] >= 100
          ? naturalMax()
          : Math.min(128, Math.max(32, Math.round(naturalMax() * steps[i] / 100)));

        if (cap >= px) {
          check('sizePct', steps[i]);
          break;
        }
      }
    }

    if (framesIn >= 2 && request.frames >= 100) {
      nearestPreset('framesPct', Math.round(framesOut / framesIn * 100), 10);
    }

    if (request.colors === '100') {
      check('colorsPct', 100);
    }
  }

  function showError(message) {
    error.textContent = message;
    error.hidden = false;
  }

  function convert(file) {
    if (!file) return;

    // a control changed while a conversion is running: run again with
    // the latest settings once it finishes, instead of dropping the click
    if (busy) {
      pending = true;

      return;
    }

    busy = true;
    error.hidden = true;

    // what the user asked for, before the algorithm has its say
    var request = {
      frames: +preset('framesPct'),
      size: +preset('sizePct'),
      colors: preset('colorsPct')
    };

    var body = new FormData();
    body.append('file', file);
    body.append('mode', mode());

    if (frameCap() > 0) body.append('maxFrames', frameCap());
    if (sizeCap() > 0) body.append('maxSide', sizeCap());
    if (colorsCap() > 0) body.append('maxColors', colorsCap());

    fetch('/convert', { method: 'POST', body: body })
      .then(function (r) {
        return r.json().then(function (j) { return { ok: r.ok, j: j }; });
      })
      .then(function (res) {
        if (!res.ok) throw new Error(res.j.error || 'Conversion failed');
        showResult(res.j.report, res.j.data, file.name, request);
      })
      .catch(function (err) {
        showError(err.message);
      })
      .finally(function () {
        busy = false;

        if (pending) {
          pending = false;
          convert(currentFile);
        }
      });
  }

  function onFile(file) {
    if (!file) return;

    currentFile = file;
    fileName.textContent = file.name + ', ' + fmtKB(file.size);
    fileName.hidden = false;
    resetBtn.hidden = false;

    // a new file gets a fresh proposal: presets back to uncapped and active
    ['framesPct', 'colorsPct', 'sizePct'].forEach(function (name) {
      document.querySelectorAll('input[name="' + name + '"]').forEach(function (r) {
        r.disabled = false;
      });
    });

    resetPresets();
    convert(file);
  }

  function resetPresets() {
    check('framesPct', 100);
    check('colorsPct', 100);
    check('sizePct', 100);
    updateLabels();
  }

  function check(name, value) {
    document.querySelector('input[name="' + name + '"][value="' + value + '"]').checked = true;
  }

  fileInput.addEventListener('change', function () {
    onFile(fileInput.files[0]);
  });

  document.querySelectorAll(
    'input[name="mode"], input[name="framesPct"], input[name="sizePct"], input[name="colorsPct"]'
  ).forEach(function (radio) {
    radio.addEventListener('change', function () {
      updateLabels();

      if (currentFile) convert(currentFile);
    });
  });

  updateLabels();

  resetBtn.addEventListener('click', function () {
    if (!currentFile) return;

    resetPresets();
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
