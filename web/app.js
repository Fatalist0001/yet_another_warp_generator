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

  // ---------- лог статуса ----------
  var _statusN = 0;
  function statusLine(msg, tag) {
    tag = tag || 'info';
    var t = new Date().toLocaleTimeString('ru-RU', { hour12: false });
    var line = document.createElement('div');
    line.className = 'line fade-in ' + tag;
    line.innerHTML =
      '<span class="time">[' + t + ']</span>' +
      '<span class="code">' +
        (tag === 'ok' ? '[ok]' :
         tag === 'warn' ? '[--]' :
         tag === 'err' ? '[err]' : '[info]') +
      '</span>' +
      '<span class="msg">' + msg + '</span>';
    els.statusBox.appendChild(line);
    els.statusBox.scrollTop = els.statusBox.scrollHeight;
    _statusN++;
    return _statusN;
  }

  function clearStatus() { els.statusBox.innerHTML = ''; _statusN = 0; }


  // Текст ошибки API: код + понятное пояснение (не просто «ошибка»).
  function apiErrorText(e) {
    if (!e) return 'неизвестная ошибка';
    var msg = (e && e.message) || String(e);
    if (e.code === 'NETWORK_ERROR') {
      return 'сервис недоступен: ' + msg.replace(/^Сервер недоступен: /, '');
    }
    return msg + (e.code ? ' [' + e.code + ']' : '');
  }

  // ---------- сегменты (portMode / i1Profile / junkPreset) ----------
  function bindSegment(seg, key) {
    var btns = seg.querySelectorAll('button');
    if (btns.length === 0) btns = seg._children || [];
    function set(val) {
      for (var i = 0; i < btns.length; i++) btns[i].classList.toggle('active', btns[i].dataset.val === val);
      localStorage.setItem(key, val);
    }
    var saved = localStorage.getItem(key);
    set(saved || (btns[0] && btns[0].dataset.val) || 'warpgen');
    seg.addEventListener('click', function (e) {
      var b = e.target && e.target.closest ? e.target.closest('button') : null;
      if (!b) return;
      set(b.dataset.val);
      updateReadout();
    });
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
        '<div class="rtt">' + (p.ok ? 'жив' : 'мёртв') +
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
    statusLine('проверка DNS-пресетов…', 'info');
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
      statusLine('DNS проверены: живых ' + alive + ' из ' + presets.length +
        ' — в конфиг подставляются ответившие IP', alive ? 'ok' : 'warn');
      return presets;
    }).catch(function (e) {
      els.dnsState.textContent = 'не удалось';
      statusLine('Проверка DNS не удалась: ' + apiErrorText(e) +
        '. Пресет останется: адреса впишутся без проверки.', 'warn');
      return null;
    }).then(function (r) {
      els.dnsPreset.disabled = false;
      els.accDnsBtn.disabled = false;
      els.accDnsBtn.textContent = 'проверить';
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
    els.accState.classList.remove('state');
    els.accInfo.innerHTML = '';
    els.accInfo.classList.add('kv');
    var kv = function (k, v, mono) {
      var row = document.createElement('div');
      row.className = 'kv-row';
      row.innerHTML = '<div class="k">' + escapeHtml(k) + '</div>' +
        '<div class="v' + (mono ? ' mono' : '') + '">' + escapeHtml(v) + '</div>';
      els.accInfo.appendChild(row);
    };
    kv('endpoint', acc.endpoint, true);
    kv('publicKey', acc.serverPublicKey, true);
    kv('IPv4', acc.addressV4, true);
    kv('IPv6', acc.addressV6, true);
    openSheet();
  }

  function doAccount() {
    statusLine('получаю аккаунт WARP на сервере…', 'info');
    return W.API.account().then(function (acc) {
      state.account = acc;
      els.accState.textContent = 'готов';
      els.accState.classList.remove('state');
      els.endpoint.value = acc.endpoint;
      showAccount();
      statusLine('аккаунт получен: ' + acc.addressV4 + ' · ' + acc.endpoint, 'ok');
      return acc;
    }).catch(function (e) {
      els.accState.textContent = 'ошибка';
      els.accState.classList.add('state');
      statusLine('не удалось получить аккаунт: ' + apiErrorText(e), 'err');
      return null;
    });
  }

  // ---------- endpoint ----------
  function endpointValue() {
    var v = els.endpoint.value.trim();
    return v || W.randomEndpoint({ widePorts: localStorage.getItem('warpgen_portmode') === 'wide' });
  }

  function rollEndpoint() {
    var ep = W.randomEndpoint({ widePorts: localStorage.getItem('warpgen_portmode') === 'wide' });
    els.endpoint.value = ep;
    updateReadout();
    return ep;
  }

  // ---------- параметры интерфейса ----------
  function bindSlider(slider, valEl, def) {
    slider.min = W.LIMITS[slider.id].min;
    slider.max = W.LIMITS[slider.id].max;
    var saved = localStorage.getItem(slider.id);
    slider.value = saved || def;
    valEl.textContent = slider.value;
    slider.addEventListener('input', function () {
      valEl.textContent = slider.value;
      localStorage.setItem(slider.id, slider.value);
      updateReadout();
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
      out.push('<span class="line ' + cls + '" style="--i:' + i + '">' +
        escapeHtml(line) + '</span>');
    }
    els.confOut.innerHTML = out.join('\n');
  }

  // «приборная» таблица на главном: текущее состояние без обращения к сети
  function updateReadout() {
    if (els.paramEp) els.paramEp.textContent = els.endpoint.value.trim() || '—';
    if (els.paramDns) {
      var sel = els.dnsPreset.value;
      var txt = '—';
      if (sel === 'custom') {
        txt = els.dnsCustom.value.trim() || 'своё';
      } else {
        var ips = state.dns.ips[sel];
        if (ips && ips.length) {
          txt = ips.slice(0, 2).join(', ');
        } else {
          for (var i = 0; i < W.DNSPresets.length; i++) {
            if (W.DNSPresets[i].id === sel) { txt = W.DNSPresets[i].label; break; }
          }
        }
      }
      els.paramDns.textContent = txt;
    }
    if (els.paramMtu) {
      els.paramMtu.textContent = 'MTU ' + (els.mtu.value || '1280') +
        ' · KA ' + (els.keepalive.value || '25');
    }
    if (els.paramI1) {
      els.paramI1.textContent = (localStorage.getItem('warpgen_i1profile') || 'quic').toUpperCase();
    }
  }

  // ---------- генерация ----------
  function doGenerate() {
    if (state.loading) return Promise.resolve(null);
    state.loading = true;
    els.genBtn.disabled = true;
    els.genBtn.textContent = 'генерирую…';
    clearStatus();

    var accP = state.account ? Promise.resolve(state.account) : doAccount();
    return accP.then(function (acc) {
      if (!acc) {
        statusLine('аккаунт недоступен — конфиг не собран', 'err');
        return null;
      }
      state.account = acc;
      statusLine('собираю .conf (WARP-safe инварианты, I1, junk)…', 'info');
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
          statusLine('конфиг не собран:', 'err');
          res.errors.forEach(function (er) { statusLine('  • ' + er, 'err'); });
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
          warnHtml = '<div class="note">Внимание: ' + res.warnings.map(escapeHtml).join('; ') + '</div>';
        }
        statusLine('конфиг готов — скопируйте или скачайте' + warnHtml,
          res.warnings && res.warnings.length ? 'warn' : 'ok');
        return res;
      });
    }).catch(function (e) {
      statusLine('ошибка генерации: ' + apiErrorText(e), 'err');
      return null;
    }).then(function (r) {
      state.loading = false;
      els.genBtn.disabled = false;
      els.genBtn.textContent = 'сгенерировать';
      return r;
    });
  }

  // ---------- копирование и скачивание ----------
  function copyConf() {
    if (!state.lastConf) return;
    var done = function () { statusLine('конфиг скопирован в буфер обмена', 'ok'); };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(state.lastConf).then(done, function () {
        fallbackCopy(); done();
      });
    } else { fallbackCopy(); done(); }
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
    var blob = new Blob([state.lastConf], { type: 'application/octet-stream' });
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url;
    a.download = 'warpgen.conf';
    document.body.appendChild(a);
    a.click();
    if (a.remove) a.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
    statusLine('warpgen.conf скачан', 'ok');
  }

  // ---------- инициализация ----------
  var _ = function () {
    var port = W.EndpointPresets.defaultPort;
    els.endpoint.value = W.randomEndpoint({ port: port });
    bindSegment(els.portMode, 'warpgen_portmode');
    bindSegment(els.i1Profile, 'warpgen_i1profile');
    bindSegment(els.junkPreset, 'warpgen_junk');
    bindSlider(els.mtu, els.mtuVal, W.LIMITS.mtu.def);
    bindSlider(els.keepalive, els.kaVal, W.LIMITS.keepalive.def);
    els.i1Profile.addEventListener('click', function (e) {
      var b = e.target.closest('button');
      if (!b) return;
      els.i1HostRow.classList.toggle('hidden', b.dataset.val !== 'quic');
    });
    els.i1HostRow.classList.toggle('hidden', localStorage.getItem('warpgen_i1profile') !== 'quic');
    fillDnsSelect();
    updateReadout();
    updateDnsUI();
    els.dnsPreset.addEventListener('change', function () { updateDnsUI(); updateReadout(); });

    els.endpoint.addEventListener('input', updateReadout);
    els.dnsCustom.addEventListener('input', updateReadout);
    els.settingsTrigger.addEventListener('click', openSettings);
    els.settingsClose.addEventListener('click', closeSettings);
    els.settingsBackdrop.addEventListener('click', closeSettings);
    els.sheetClose.addEventListener('click', closeSheet);
    els.genBtn.addEventListener('click', function () { return doGenerate(); });
    els.accBtn.addEventListener('click', function () { return doAccount(); });
    els.accDnsBtn.addEventListener('click', function () { return dnsPresets(); });
    els.epBtn.addEventListener('click', rollEndpoint);
    els.confCopyBtn.addEventListener('click', copyConf);
    els.confCopy.addEventListener('click', copyConf);
    els.confDownload.addEventListener('click', downloadConf);

    document.body.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') { closeSettings(); closeSheet(); }
    });

    window.__warpgen = {
      state: state, els: els,
      doAccount: doAccount, dnsPresets: dnsPresets,
      doGenerate: doGenerate, renderConf: renderConf,
      currentAccount: currentAccount, rollEndpoint: rollEndpoint
    };
  };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', _);
  } else {
    _();
  }
})();
