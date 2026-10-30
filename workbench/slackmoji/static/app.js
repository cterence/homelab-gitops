(function () {
  var fileInput = document.getElementById('file');
  var fileName = document.getElementById('file-name');
  var drop = document.getElementById('drop');
  var downloadBtn = document.getElementById('download-btn');
  var previewCol = document.getElementById('preview-col');
  var preview = document.getElementById('preview');
  var error = document.getElementById('error');
  var resultMeta = document.getElementById('result-meta');
  var framesSlider = document.getElementById('frames-slider');
  var colorsSlider = document.getElementById('colors-slider');
  var sizeSlider = document.getElementById('size-slider');
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

  // a slider the user has dragged is theirs until Reset or a new file;
  // untouched sliders sit at the algorithm's outcome
  var touched = { frames: false, colors: false, size: false };

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
    return Math.min(+sizeSlider.dataset.inPx || 128, 128);
  }

  // a slider at its maximum means uncapped
  function frameCap() {
    return +framesSlider.value < +framesSlider.max ? +framesSlider.value : 0;
  }

  function sizeCap() {
    return +sizeSlider.value < +sizeSlider.max ? +sizeSlider.value : 0;
  }

  function colorsCap() {
    return +colorsSlider.value < +colorsSlider.max ? +colorsSlider.value : 0;
  }

  function updateLabels() {
    framesHint.textContent = framesSlider.disabled
      ? 'all'
      : (+framesSlider.max < 2
          ? 'single frame'
          : (frameCap() ? frameCap() + ' of ' + framesSlider.max : 'all ' + framesSlider.max));
    colorsHint.textContent = colorsSlider.disabled
      ? 'best'
      : (colorsCap() ? colorsCap() + ' colors' : 'uncapped');
    sizeHint.textContent = sizeSlider.disabled
      ? '128 px max'
      : (sizeCap() ? sizeCap() + ' px' : naturalMax() + ' px max');
  }

  function showResult(report) {
    error.hidden = true;
    previewCol.hidden = false;

    preview.src = report.dataUrl;
    preview.alt = 'Converted preview, ' + report.width + ' by ' + report.height + ' pixels';
    preview.hidden = false;

    resultMeta.textContent = report.passthrough ? 'unchanged' : 'converted';

    var px = Math.max(report.width, report.height);
    var inPx = Math.max(report.inputWidth, report.inputHeight);

    fields.size.textContent = fmtKB(report.outputSize) + ' of 128 KB';
    fields.dims.textContent = report.width + ' × ' + report.height + ' px';
    fields.frames.textContent = report.framesOut + ' of ' + report.framesIn;
    fields.colors.textContent = plural(report.colors, 'color');
    fields.input.textContent = fmtKB(report.inputSize);

    // red marks what was sacrificed relative to the input
    fields.dims.classList.toggle('dropped', !report.passthrough && px < inPx);
    fields.frames.classList.toggle('dropped', report.framesOut < report.framesIn);

    // sliders describe the file's ranges...
    framesSlider.max = report.framesIn;
    colorsSlider.max = 255;
    sizeSlider.dataset.inPx = inPx;
    sizeSlider.min = Math.min(32, naturalMax());
    sizeSlider.max = naturalMax();
    [framesSlider, colorsSlider, sizeSlider].forEach(function (s) {
      s.disabled = false;
    });

    // ...and untouched sliders sit at the algorithm's outcome
    if (!touched.frames) framesSlider.value = report.framesOut;
    if (!touched.size) sizeSlider.value = px;
    if (!touched.colors) colorsSlider.value = report.colors;

    updateLabels();

    current = { dataUrl: report.dataUrl, name: report.name };
    downloadBtn.hidden = false;
    downloadBtn.textContent = 'Download ' + (report.dataUrl.indexOf('image/gif') !== -1 ? 'GIF' : 'PNG');
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
        showResult(Object.assign({ dataUrl: res.j.data, name: file.name }, res.j.report));
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

    resetPresets();
    convert(file);
  }

  function resetPresets() {
    touched = { frames: false, colors: false, size: false };

    framesSlider.value = framesSlider.max;
    colorsSlider.value = colorsSlider.max;
    sizeSlider.max = naturalMax();
    sizeSlider.value = sizeSlider.max;

    updateLabels();
  }

  fileInput.addEventListener('change', function () {
    onFile(fileInput.files[0]);
  });

  [framesSlider, colorsSlider, sizeSlider].forEach(function (slider) {
    var key = slider.id.split('-')[0];

    // dragging marks the slider as the user's and shows the live value;
    // releasing converts
    slider.addEventListener('input', function () {
      touched[key] = true;
      updateLabels();
    });

    slider.addEventListener('change', function () {
      if (currentFile) convert(currentFile);
    });
  });

  document.querySelectorAll('input[name="mode"]').forEach(function (radio) {
    radio.addEventListener('change', function () {
      if (!touched.size) {
        sizeSlider.max = naturalMax();
      }

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
