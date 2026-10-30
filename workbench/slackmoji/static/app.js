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
  var framesSlider = document.getElementById('max-frames');
  var framesOut = document.getElementById('max-frames-out');
  var sideSlider = document.getElementById('max-side');
  var sideOut = document.getElementById('max-side-out');

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

  function frameCap() {
    if (framesSlider.disabled || +framesSlider.value >= +framesSlider.max) return 0;
    return +framesSlider.value;
  }

  function updateSliderLabels() {
    if (framesSlider.disabled) {
      framesOut.textContent = 'all';
    } else if (+framesSlider.value >= +framesSlider.max) {
      framesOut.textContent = 'all frames';
    } else {
      framesOut.textContent = framesSlider.value + ' of ' + framesSlider.max;
    }

    sideOut.textContent = sideSlider.value + ' px';
  }

  function showResult(report, dataUrl, name) {
    error.hidden = true;
    previewCol.hidden = false;

    preview.src = dataUrl;
    preview.alt = 'Converted preview, ' + report.width + ' by ' + report.height + ' pixels';
    preview.style.width = report.width * 4 + 'px';
    preview.hidden = false;

    if (report.framesIn > 1) {
      framesSlider.disabled = false;
      framesSlider.max = report.framesIn;
    }

    var framesText;
    if (report.passthrough) {
      finding.textContent = 'Already within the limits, returned unchanged.';
    } else if (report.framesOut === report.framesIn) {
      framesText = 'all ' + plural(report.framesIn, 'frame') + ' kept';
      finding.textContent = 'Fits the limits: ' + fmtKB(report.outputSize) + ' of 128 KB, ' + framesText + '.';
    } else if (report.framesOut === requestedFrames) {
      framesText = plural(report.framesOut, 'frame') + ' of ' + plural(report.framesIn, 'frame') + ' kept';
      finding.textContent = 'Fits the limits: ' + fmtKB(report.outputSize) + ' of 128 KB, ' + framesText +
        ' at your frame cap.';
    } else {
      framesText = plural(report.framesOut, 'frame') + ' of ' + plural(report.framesIn, 'frame') + ' kept';
      finding.innerHTML = 'Fits the limits: ' + fmtKB(report.outputSize) +
        ' of 128 KB, <span class="dropped">' + framesText + '</span> after quality was exhausted.';
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
    finding.textContent = 'Converting ' + file.name + '…';
    error.hidden = true;

    requestedFrames = frameCap();

    var body = new FormData();
    body.append('file', file);
    body.append('mode', mode());

    if (requestedFrames > 0) body.append('maxFrames', requestedFrames);

    if (+sideSlider.value < +sideSlider.max) body.append('maxSide', sideSlider.value);

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

  document.querySelectorAll('input[name="mode"]').forEach(function (radio) {
    radio.addEventListener('change', function () {
      if (currentFile) convert(currentFile);
    });
  });

  framesSlider.addEventListener('change', function () {
    updateSliderLabels();
    if (currentFile) convert(currentFile);
  });

  sideSlider.addEventListener('change', function () {
    updateSliderLabels();
    if (currentFile) convert(currentFile);
  });

  updateSliderLabels();

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
