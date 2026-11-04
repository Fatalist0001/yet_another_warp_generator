// app.js — связка UI с ядром window.Warpgen.
// Слой страницы: настройки в localStorage, аккаунт в памяти (ключи не показываем),
// конфиг собирается ядром. Экспортирует window.__warpgen для офлайн-тестов.
(function () {
  'use strict';
  var W = window.Warpgen;
  if (!W) { console.error('[warpgen] ядро не загружено'); return; }

  function $(id) { return document.getElementById(id); }

  // ---------- состояние (без секретов в UI/localStorage) ----------
  var state = { account: null, dns: { id: null, ips: {} }, lastConf: '', loading: false };

  // ---------- элементы ----------
  var els = {
    settingsTrigger: $('settingsTrigger'),
    settingsPanel: $('settingsPanel'),
    settingsBackdrop: $('settingsBackdrop'),
    settingsClose: $('settingsClose'),
    sheet: $('sheet'),
    sheetClose: $('sheetClose'),
    sheetTitle: $('sheetTitle'),

    endpoint: $('endpoint'),
    epBtn: $('epBtn'),
    portMode: $('portMode'),
    i1Profile: $('i1Profile'),
    i1Host: $('i1Host'),
    i1HostRow: $('i1HostRow'),
    junkPreset: $('junkPreset'),
    mtu: $('mtu'),
    mtuVal: $('mtuVal'),
    keepalive: $('keepalive'),
    kaVal: $('kaVal'),
    buildNote: $('buildNote'),

    genBtn: $('genBtn'),
    paramEp: $('paramEp'),
    paramDns: $('paramDns'),
    paramMtu: $('paramMtu'),
    paramI1: $('paramI1'),
    paramsRow: $('paramsRow'),

    statusBox: $('statusBox'),
    confBlock: $('confBlock'),
    confOut: $('confOut'),
    confCopyBtn: $('confCopyBtn'),
    confCopy: $('confCopy'),
    confDownload: $('confDownload'),

    accBtn: $('accBtn'),
    accDnsBtn: $('accDnsBtn'),
    accState: $('accState'),
    accInfo: $('accInfo'),
    dnsPreset: $('dnsPreset'),
    dnsCustom: $('dnsCustom'),
    dnsCustomRow: $('dnsCustomRow'),
    dnsList: $('dnsList'),
    dnsState: $('dnsState')
  };

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  // ---------- панели ----------
  function openSettings() {
    els.settingsPanel.classList.add('open');
    els.settingsBackdrop.classList.add('open');
    els.settingsTrigger.classList.add('open');
  }

  function closeSettings() {
    els.settingsPanel.classList.remove('open');
    els.settingsBackdrop.classList.remove('open');
    els.settingsTrigger.classList.remove('open');
  }

  function openSheet() { els.sheet.classList.add('open'); }
  function closeSheet() { els.sheet.classList.remove('open'); }

  // ---------- статус-плашка ----------
  function status(html, tag) {
    els.statusBox.className = 'status ' + (tag || 'info');
    els.statusBox.innerHTML = html;
    openSheet();
  }

  // Текст ошибки API: код + понятное пояснение (не просто «ошибка»).
  function apiErrorText(e) {
    if (!e) return 'неизвестная ошибка';
    var msg = (e && e.message) || String(e);
    if (e.code === 'NETWORK_ERROR') {
      return 'сервис недоступен: ' + msg.replace(/^Сервер недоступен: /, '');
    }
    return msg + (e.code ? ' [' + e.code + ']' : '');
  }

  // ---------- DNS ----------
  function fillDnsSelect(presets) {
    presets = presets || W.DNSPresets;
    els.dnsPreset.innerHTML = '';
    presets.forEach(function (p) {
      var op = document.createElement('option');
      op.value = p.id;
      var t = p.label;
      if (typeof p.rttMs === 'number') t += ' (' + p.rttMs + ' мс)';
      if (p.ok === false) { t += ' ✕'; op.disabled = true; }
      op.textContent = t;
      els.dnsPreset.appendChild(op);
    });
  }

  function updateDnsUI() {
    var custom = els.dnsPreset.value === 'custom';
    els.dnsCustomRow.classList.toggle('hidden', !custom);
    if (custom) { els.dnsState.textContent = 'введите IP вручную'; return; }
    var label = els.dnsPreset.value;
    for (var i = 0; i < W.DNSPresets.length; i++) {
      if (W.DNSPresets[i].id === els.dnsPreset.value) { label = W.DNSPresets[i].label; break; }
    }
    els.dnsState.textContent = 'выбран: ' + label;
  }

  function renderDnsList(presets) {
    els.dnsList.innerHTML = '';
    presets.forEach(function (p) {
      var ipStr = (p.liveIps && p.liveIps.length) ? p.liveIps.join(', ')
        : (p.ips && p.ips.length ? p.ips.join(', ') : '—');
      var el = document.createElement('div');
      el.className = 'dns-item' + (p.ok ? '' : ' dead');
      el.innerHTML = '<div class="name">' + escapeHtml(p.label) + '</div>' +
        '<div class="ip">' + escapeHtml(ipStr) + '</div>' +
        '<div class="note">' + (p.ok ? 'жив' : 'мёртв') +
        (p.rttMs != null ? ' · ' + p.rttMs + ' мс' : '') + '</div>';
      el.addEventListener('click', function () {
        if (!p.ok) return;
        els.dnsPreset.value = p.id;
        localStorage.setItem('warpgen_dns', p.id);
        els.dnsList.classList.add('hidden');
        updateDnsUI();
      });
      els.dnsList.appendChild(el);
    });
  }

  // Проверка DNS выполняется сервером; живые IP запоминаем для подстановки.
  function dnsPresets() {
    els.dnsPreset.disabled = true;
    els.dnsState.textContent = 'проверка…';
    els.dnsList.classList.add('hidden');
    els.accDnsBtn.disabled = true;
    els.accDnsBtn.textContent = 'проверяю…';
    return W.API.dns().then(function (presets) {
      state.dns.ips = {};
      presets.forEach(function (p) {
        if (p.liveIps && p.liveIps.length) state.dns.ips[p.id] = p.liveIps.slice();
      });
      var alive = 0;
      var firstAlive = null;
      presets.forEach(function (p) {
        if (p.ok) { alive++; if (!firstAlive) firstAlive = p; }
      });
      fillDnsSelect(presets);
      if (firstAlive) {
        els.dnsPreset.value = firstAlive.id;
        localStorage.setItem('warpgen_dns', firstAlive.id);
      }
      renderDnsList(presets);
      els.dnsList.classList.remove('hidden');
      els.dnsCustomRow.classList.add('hidden');
      els.dnsState.textContent = 'проверено: ' + alive + ' из ' + presets.length;
      status('DNS проверены сервером: живых ' + alive + ' из ' + presets.length +
        '. В конфиг подставляются ответившие IP.', alive ? 'ok' : 'warn');
      return presets;
    }).catch(function (e) {
      els.dnsState.textContent = 'не удалось';
      status('Проверка DNS не удалась: ' + apiErrorText(e) +
        '<div class="note">Можно выбрать пресет вручную — адреса впишутся без проверки.</div>', 'warn');
      return null;
    }).then(function (r) {
      els.dnsPreset.disabled = false;
      els.accDnsBtn.disabled = false;
      els.accDnsBtn.textContent = '🩺 проверить';
      return r;
    });
  }

  // Значение DNS для конфига: живые IP с сервера, своё поле или хостнейм (DoH в ядре).
  function dnsValue() {
    var sel = els.dnsPreset.value;
    if (sel === 'custom') return els.dnsCustom.value.trim();
    var ips = state.dns.ips[sel];
    if (ips && ips.length) return ips.join(', ');
    var preset = null;
    for (var i = 0; i < W.DNSPresets.length; i++) {
      if (W.DNSPresets[i].id === sel) { preset = W.DNSPresets[i]; break; }
    }
    return (preset && preset.value) || sel;
  }

  // ---------- аккаунт ----------
  function currentAccount() { return state.account || null; }

  function showAccount() {
    var acc = currentAccount();
    if (!acc) return;
    els.accState.textContent = 'готов';
    els.accInfo.innerHTML =
      '<div class="kv-row"><span class="k">PublicKey</span><span class="v">' + escapeHtml(acc.serverPublicKey || '—') + '</span></div>' +
      '<div class="kv-row"><span class="k">IPv4</span><span class="v">' + escapeHtml(acc.addressV4 || '—') + '</span></div>' +
      '<div class="kv-row"><span class="k">IPv6</span><span class="v">' + escapeHtml(acc.addressV6 || '—') + '</span></div>' +
      '<div class="kv-row"><span class="k">Endpoint</span><span class="v">' + escapeHtml(acc.endpoint || '—') + '</span></div>' +
      '<div class="note">Приватный ключ не показывается и никуда не сохраняется в браузере.</div>';
  }

  function doAccount() {
    if (state.account) { showAccount(); return Promise.resolve(state.account); }
    els.accBtn.disabled = true;
    els.accState.textContent = 'регистрация…';
    status('<span class="loader"></span> создаю аккаунт WARP на сервере…', 'info');
    return W.API.account().then(function (acc) {
      state.account = acc;
      showAccount();
      if (acc.endpoint && !els.endpoint.value.trim()) els.endpoint.value = acc.endpoint;
      status('Аккаунт WARP получен. Можно генерировать конфиг.', 'ok');
      return acc;
    }).catch(function (e) {
      els.accState.textContent = 'ошибка';
      status('Не удалось получить аккаунт: ' + apiErrorText(e), 'err');
      return null;
    }).then(function (r) {
      els.accBtn.disabled = false;
      els.accBtn.textContent = '🔐 Аккаунт';
      return r;
    });
  }

  // ---------- endpoint ----------
  function portModeValue() {
    return localStorage.getItem('warpgen_portmode') || els.portMode.value || 'wide';
  }

  function rollEndpoint() {
    var ep = W.randomEndpoint({ widePorts: portModeValue() === 'wide' });
    els.endpoint.value = ep;
    return ep;
  }

  function endpointValue() {
    return els.endpoint.value.trim() || rollEndpoint();
  }

  // ---------- настройки ----------
  function loadSettings() {
    var q = new URLSearchParams(location.search || '');
    els.endpoint.value = q.get('endpoint') || '';
    els.portMode.value = localStorage.getItem('warpgen_portmode') || 'wide';
    els.i1Profile.value = localStorage.getItem('warpgen_i1profile') || 'quic';
    els.junkPreset.value = localStorage.getItem('warpgen_junk') || 'warpgen';
    els.i1Host.value = localStorage.getItem('warpgen_i1host') || '';
    fillDnsSelect();
    els.dnsPreset.value = localStorage.getItem('warpgen_dns') || W.DNSPresets[0].id;
    els.dnsCustom.value = localStorage.getItem('warpgen_dns_custom') || '';
    if (!els.endpoint.value) rollEndpoint();
  }

  function bindSlider(rangeEl, valEl, limits, storageKey) {
    rangeEl.min = limits.min;
    rangeEl.max = limits.max;
    var v = localStorage.getItem(storageKey);
    rangeEl.value = v !== null ? Number(v) : limits.def;
    valEl.textContent = rangeEl.value;
    rangeEl.addEventListener('input', function () {
      valEl.textContent = rangeEl.value;
      localStorage.setItem(storageKey, String(rangeEl.value));
    });
  }

  // ---------- рендер конфига ----------
  function renderConf(confText) {
    var lines = String(confText).split('\n');
    var out = [];
    for (var i = 0; i < lines.length; i++) {
      var line = lines[i];
      var cls = 'val';
      if (/^\[.*\]$/.test(line)) cls = 'section';
      else if (/^[A-Za-z]/.test(line)) cls = 'key';
      out.push('<span class="line ' + cls + '">' + escapeHtml(line) + '</span>');
    }
    els.confOut.innerHTML = out.join('\n');
  }

  // ---------- генерация ----------
  function doGenerate() {
    if (state.loading) return Promise.resolve(null);
    state.loading = true;
    els.genBtn.disabled = true;
    els.genBtn.textContent = 'генерирую…';
    status('<span class="loader"></span> собираю конфиг…', 'info');

    var accP = state.account ? Promise.resolve(state.account) : doAccount();
    return accP.then(function (acc) {
      if (!acc) {
        status('Аккаунт недоступен — конфиг не собран. Проверьте соединение с сервером.', 'err');
        return null;
      }
      state.account = acc;
      var input = {
        account: acc,
        endpoint: endpointValue(),
        mtu: Number(els.mtu.value),
        keepalive: Number(els.keepalive.value),
        i1Profile: localStorage.getItem('warpgen_i1profile') || els.i1Profile.value || 'quic',
        i1Host: els.i1Host.value.trim(),
        junk: localStorage.getItem('warpgen_junk') || els.junkPreset.value || 'warpgen',
        dns: dnsValue()
      };
      return W.CONFIG.generate(input).then(function (res) {
        if (!res.ok) {
          status('Конфиг не собран:<br>• ' + res.errors.map(escapeHtml).join('<br>• '), 'err');
          return null;
        }
        state.lastConf = res.conf;
        renderConf(res.conf);
        els.paramEp.textContent = input.endpoint;
        els.paramDns.textContent = (input.dns || '—').split(',').slice(0, 2).join(', ');
        els.paramMtu.textContent = 'MTU ' + input.mtu;
        els.paramI1.textContent = 'I1: ' + input.i1Profile;
        els.confBlock.classList.remove('hidden');
        var warnHtml = '';
        if (res.warnings && res.warnings.length) {
          warnHtml = '<div class="note">⚠ ' + res.warnings.map(escapeHtml).join('<br>⚠ ') + '</div>';
        }
        status('Конфиг готов — скопируйте или скачайте.' + warnHtml,
          res.warnings && res.warnings.length ? 'warn' : 'ok');
        return res;
      });
    }).catch(function (e) {
      status('Ошибка генерации: ' + apiErrorText(e), 'err');
      return null;
    }).then(function (r) {
      state.loading = false;
      els.genBtn.disabled = false;
      els.genBtn.textContent = 'Сгенерировать';
      return r;
    });
  }

  // ---------- экспорт для тестов ----------
  window.__warpgen = {
    state: state,
    els: els,
    doAccount: doAccount,
    dnsPresets: dnsPresets,
    doGenerate: doGenerate,
    renderConf: renderConf
  };

  // ---------- копирование и скачивание ----------
  function copyConf() {
    if (!state.lastConf) return;
    var done = function () { status('Конфиг скопирован в буфер обмена.', 'ok'); };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(state.lastConf).then(done, function () {
        fallbackCopy();
        done();
      });
    } else {
      fallbackCopy();
      done();
    }
  }

  function fallbackCopy() {
    var ta = document.createElement('textarea');
    ta.value = state.lastConf;
    document.body.appendChild(ta);
    if (ta.select) ta.select();
    try { document.execCommand('copy'); } catch (e) { /* старый браузер — молча */ }
    if (ta.remove) ta.remove();
  }

  function downloadConf() {
    if (!state.lastConf) return;
    // octet-stream, не text/plain: иначе браузер дописывает .txt к имени.
    var blob = new Blob([state.lastConf], { type: 'application/octet-stream' });
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url;
    a.download = 'warpgen.conf';
    document.body.appendChild(a);
    a.click();
    if (a.remove) a.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
    status('Файл warpgen.conf скачан.', 'ok');
  }

  // ---------- обработчики ----------
  els.settingsTrigger.addEventListener('click', function () {
    if (els.settingsPanel.classList.contains('open')) closeSettings(); else openSettings();
  });
  els.settingsBackdrop.addEventListener('click', closeSettings);
  els.settingsClose.addEventListener('click', closeSettings);
  els.sheetClose.addEventListener('click', closeSheet);
  els.genBtn.addEventListener('click', doGenerate);
  els.accBtn.addEventListener('click', doAccount);
  els.accDnsBtn.addEventListener('click', dnsPresets);
  els.confCopyBtn.addEventListener('click', copyConf);
  els.confCopy.addEventListener('click', copyConf);
  els.confDownload.addEventListener('click', downloadConf);
  els.epBtn.addEventListener('click', function () {
    status('Случайный endpoint из диапазонов WARP: ' + rollEndpoint(), 'ok');
  });

  els.dnsPreset.addEventListener('change', function () {
    localStorage.setItem('warpgen_dns', els.dnsPreset.value);
    els.dnsList.classList.add('hidden');
    updateDnsUI();
  });
  els.dnsCustom.addEventListener('input', function () {
    localStorage.setItem('warpgen_dns_custom', els.dnsCustom.value.trim());
  });
  els.i1Profile.addEventListener('change', function () {
    localStorage.setItem('warpgen_i1profile', els.i1Profile.value);
    els.i1HostRow.classList.toggle('hidden', els.i1Profile.value !== 'quic');
  });
  els.i1Host.addEventListener('input', function () {
    localStorage.setItem('warpgen_i1host', els.i1Host.value.trim());
  });
  els.junkPreset.addEventListener('change', function () {
    localStorage.setItem('warpgen_junk', els.junkPreset.value);
  });
  els.portMode.addEventListener('change', function () {
    localStorage.setItem('warpgen_portmode', els.portMode.value);
  });

  window.addEventListener('keydown', function (e) {
    if (e && e.key === 'Escape') { closeSettings(); closeSheet(); }
  });

  // ---------- старт ----------
  loadSettings();
  bindSlider(els.mtu, els.mtuVal, W.LIMITS.mtu, 'warpgen_mtu');
  bindSlider(els.keepalive, els.kaVal, W.LIMITS.keepalive, 'warpgen_keepalive');
  els.i1HostRow.classList.toggle('hidden', els.i1Profile.value !== 'quic');
  updateDnsUI();
})();
