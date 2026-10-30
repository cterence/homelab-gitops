(function () {
  var form = document.getElementById('convert-form');
  var fileInput = document.getElementById('file');
  var fileName = document.getElementById('file-name');
  var drop = document.getElementById('drop');
  var convertBtn = document.getElementById('convert-btn');
  var sampleBtn = document.getElementById('sample-btn');
  var downloadBtn = document.getElementById('download-btn');
  var stageCap = document.getElementById('stage-cap');
  var preview = document.getElementById('preview');
  var finding = document.getElementById('finding');
  var error = document.getElementById('error');
  var resultMeta = document.getElementById('result-meta');

  var fields = {
    size: document.getElementById('f-size'),
    dims: document.getElementById('f-dims'),
    frames: document.getElementById('f-frames'),
    colors: document.getElementById('f-colors'),
    input: document.getElementById('f-input')
  };

  var current = null;

  function fmtKB(bytes) {
    return (bytes / 1024).toFixed(1).replace(/\.0$/, '') + ' KB';
  }

  function plural(n, noun) {
    return n + ' ' + noun + (n === 1 ? '' : 's');
  }

  function setBusy(busy) {
    convertBtn.disabled = busy;
    convertBtn.textContent = busy ? 'Converting…' : 'Convert to emoji';
  }

  function showResult(report, dataUrl, name) {
    error.hidden = true;

    preview.src = dataUrl;
    preview.alt = 'Converted preview, ' + report.width + ' by ' + report.height + ' pixels';
    stageCap.hidden = true;

    var framesText;
    if (report.passthrough) {
      finding.textContent = 'Already within the limits, returned unchanged.';
    } else if (report.framesOut === report.framesIn) {
      framesText = 'all ' + plural(report.framesIn, 'frame') + ' kept';
      finding.textContent = 'Fits the limits: ' + fmtKB(report.outputSize) + ' of 128 KB, ' + framesText + '.';
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

    Object.keys(fields).forEach(function (k) {
      fields[k].classList.remove('empty');
    });

    current = { dataUrl: dataUrl, name: name };
    downloadBtn.hidden = false;
    downloadBtn.textContent = 'Download ' + name.split('.').pop().toUpperCase();
  }

  function showError(message) {
    error.textContent = message;
    error.hidden = false;
  }

  function submit(file, mode) {
    if (!file) return;

    var body = new FormData();
    body.append('file', file);
    body.append('mode', mode);

    setBusy(true);

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
        setBusy(false);
      });
  }

  form.addEventListener('submit', function (e) {
    e.preventDefault();
    showError('');
    error.hidden = true;

    var mode = form.querySelector('input[name="mode"]:checked').value;

    submit(fileInput.files[0], mode);
  });

  sampleBtn.addEventListener('click', function () {
    fetch('/sample.gif')
      .then(function (r) { return r.blob(); })
      .then(function (blob) {
        var file = new File([blob], 'sample.gif', { type: 'image/gif' });
        var dt = new DataTransfer();
        dt.items.add(file);
        fileInput.files = dt.files;
        fileName.textContent = 'sample.gif, ' + fmtKB(blob.size);
        fileName.hidden = false;
        submit(file, form.querySelector('input[name="mode"]:checked').value);
      });
  });

  fileInput.addEventListener('change', function () {
    if (fileInput.files[0]) {
      fileName.textContent = fileInput.files[0].name + ', ' + fmtKB(fileInput.files[0].size);
      fileName.hidden = false;
    }
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
      fileName.textContent = file.name + ', ' + fmtKB(file.size);
      fileName.hidden = false;
    }
  });
})();
