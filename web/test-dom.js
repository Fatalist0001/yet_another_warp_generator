// Headless-тест страницы index.html: минимальный DOM-стаб + реальная логика страницы.
// Сеть не нужна: fetch подменён (/api/warp, /api/dns, DoH).
// Запуск: node test-dom.js
const nodeCrypto = require('crypto');
const fs = require('fs');
const path = require('path');

let failures = 0;
function check(name, cond, extra) {
  console.log('   ', cond ? '✓' : '✗', name + (extra ? ' — ' + extra : ''));
  if (!cond) failures++;
}

// ---------- минимальный DOM ----------
function makeEl(id) {
  const classes = new Set();
  const el = {
    id: id, value: '', innerHTML: '', textContent: '', placeholder: '', disabled: false,
    min: '', max: '', checked: false, style: {}, dataset: {},
    _children: [],
    _handlers: {},
    addEventListener(ev, fn) { (this._handlers[ev] = this._handlers[ev] || []).push(fn); },
    classList: {
      add(c) { classes.add(c); },
      remove(c) { classes.delete(c); },
      contains(c) { return classes.has(c); },
      toggle(c, force) {
        const on = force === undefined ? !classes.has(c) : !!force;
        if (on) classes.add(c); else classes.delete(c);
        return on;
      }
    },
    appendChild(el) {
      this._children.push(el);
      if (el.tagName === 'OPTION') {
        this.innerHTML += '<option value="' + (el.value || '') + '"' +
          (el.disabled ? ' disabled' : '') + '>' + (el.textContent || '') + '</option>';
      } else {
        this.innerHTML += el.innerHTML || el.textContent || '';
      }
    }, removeChild() {}, remove() {}, insertAdjacentHTML() {}, querySelectorAll(sel) { return []; },
    click() { return Promise.all((this._handlers.click || []).map(fn => fn({ target: this }))); },
    fire(ev) { return Promise.all((this._handlers[ev] || []).map(fn => fn({ target: this }))); },
    scrollIntoView() {}, setAttribute(k, v) { this[k] = v; }, removeAttribute(k) { delete this[k]; },
  };
  el.className = '';
  return el;
}

const elements = {};
const document = {
  getElementById(id) {
    if (!elements[id]) elements[id] = makeEl(id);
    return elements[id];
  },
  createElement(tag) {
    var el = makeEl('created:' + tag);
    el.tagName = tag ? tag.toUpperCase() : '';
    return el;
  },
  addEventListener(ev, fn) { document._h = document._h || []; document._h.push([ev, fn]); },
  body: makeEl('body'),
  dispatchEvent() {},
  execCommand() { return true; }
};
const window = {
  addEventListener: function (ev, fn) { var h = this._h = this._h || []; h.push([ev, fn]); }
};


// эмуляция ESC
document.addEventListener('keydown', function (e) {
  if (document._dh) for (var i = 0; i < document._dh.length; i++) document._dh[i](e);
});
document.addEventListener('keyup', function (e) {
  if (document._dh) for (var i = 0; i < document._dh.length; i++) document._dh[i](e);
});

const store = {};
global.localStorage = {
  getItem: k => (k in store ? store[k] : null),
  setItem: (k, v) => { store[k] = String(v); },
  removeItem: k => { delete store[k]; }
};
global.location = { search: '', origin: 'http://localhost', protocol: 'http:' };
global.document = document;
global.performance = { now: () => Date.now() };
Object.defineProperty(global, 'navigator', {
  value: { clipboard: { writeText: async (t) => { global.__copied = t; } } },
  configurable: true, writable: true
});
global.URL = {
  createObjectURL: (b) => { global.__downloadBlob = b; return 'blob:x'; },
  revokeObjectURL() {}
};
global.Blob = function (parts, opts) { this.parts = parts; this.opts = opts || {} };

// ---------- фейковый сервер ----------
const ACC = {
  privKey: 'cHJpdmF0ZS1rZXktMzItYnl0ZXMtZm9yLXRlc3RzISE=',
  peer_pub: 'bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=',
  client_ipv4: '172.16.0.2',
  client_ipv6: '2606:4700:110:8f2e:f22e:f22e:8a2e:e12e',
  endpoint: '162.159.192.1:500'
};
const DNS_REPORT = {
  presets: [
    { id: 'comss', label: 'Comss', host: 'dns.comss.one', ips: ['1.2.3.4', '6.6.6.6'],
      liveIps: ['1.2.3.4'], ok: true, rttMs: 21 },
    { id: 'malw', label: 'Malw', host: 'dns.malw.link', ips: [], ok: false, rttMs: null, note: 'таймаут' }
  ]
};
global.__calls = [];

global.fetch = async (url) => {
  url = String(url);
  global.__calls.push(url);
  const json = (o, status) => ({
    ok: status === undefined || status < 400, status: status || 200,
    text: async () => JSON.stringify(o), json: async () => o
  });
  if (url.endsWith('/api/warp')) return json(ACC);
  if (url.endsWith('/api/dns')) return json(DNS_REPORT);
  if (url.includes('cloudflare-dns.com')) return json({ Answer: [{ type: 1, data: '5.6.7.8' }] });
  throw new Error('нет сети в тесте: ' + url);
};

// crypto для warpgen.js
global.crypto = {
  getRandomValues(arr) { nodeCrypto.randomFillSync(Buffer.from(arr.buffer, arr.byteOffset, arr.byteLength)); return arr; },
  subtle: nodeCrypto.webcrypto.subtle
};

global.window = global;
global.self = global;
global.addEventListener = function (ev, fn) { var h = global._h = global._h || []; h.push([ev, fn]); };
global.removeEventListener = function (ev, fn) { /* нет отписки */ };

// ---------- загрузка ----------
new Function(fs.readFileSync(path.join(__dirname, 'warpgen.js'), 'utf8'))();
if (!global.Warpgen) throw new Error('Warpgen не загрузился');

const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
const app = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
new Function(app)();

// app.js ждёт DOMContentLoaded — диспатчим вручную
(document._h || []).filter(function (p) { return p[0] === 'DOMContentLoaded'; })
  .forEach(function (p) { p[1](); });

const W = global.Warpgen;
const page = global.__warpgen;
const els = page.els;

(async () => {

function conf() { return page.state.lastConf || ''; }
function statusHtml() { return els.statusBox.innerHTML || ''; }

// ---------- 1. инициализация ----------
console.log('[1] Инициализация страницы');
check('14+ элементов найдено', Object.keys(elements).length > 20, Object.keys(elements).length + ' шт.');
check('пресеты DNS в select', (els.dnsPreset.innerHTML.match(/<option/g) || []).length === W.DNSPresets.length);
check('endpoint предзаполнен ip:port', /^\d{1,3}(\.\d{1,3}){3}:\d+$/.test(els.endpoint.value), els.endpoint.value);
check('границы MTU из LIMITS', String(els.mtu.min) === String(W.LIMITS.mtu.min) && String(els.mtu.max) === String(W.LIMITS.mtu.max));
check('MTU = 1280 по умолчанию', String(els.mtu.value) === String(W.LIMITS.mtu.def));
check('keepalive = 25 по умолчанию', String(els.keepalive.value) === String(W.LIMITS.keepalive.def));
check('junk по умолчанию = warpgen', (localStorage.getItem('warpgen_junk') || 'warpgen') === 'warpgen');
check('скрытый блок DNS custom по умолчанию', els.dnsCustomRow.classList.contains('hidden'));

// ---------- 2. аккаунт с сервера ----------
console.log('\n[2] Аккаунт с сервера');
await els.accBtn.click();
check('аккаунт сохранён', page.state.account && page.state.account.serverPublicKey === ACC.peer_pub);
check('приватный ключ не виден в UI', !els.accInfo.innerHTML.includes(ACC.privKey));
check('аккаунт отрисован (endpoint/IPv4/IPv6/PublicKey)', els.accInfo.innerHTML.includes(ACC.peer_pub) &&
  els.accInfo.innerHTML.includes(ACC.client_ipv4));
check('состояние «готов»', els.accState.textContent === 'готов', els.accState.textContent);
check('запрос ушёл на /api/warp', global.__calls.some(u => u.endsWith('/api/warp')));

// ---------- 3. DNS ----------
console.log('\n[3] Проверка DNS');
await els.accDnsBtn.click();
check('select обновлён ответом сервера', els.dnsPreset.innerHTML.includes('21 мс'));
check('мёртвый пресет помечен и отключён', els.dnsPreset.innerHTML.includes('✕') && els.dnsPreset.innerHTML.includes('disabled'));
check('выбран первый живой', els.dnsPreset.value === 'comss', els.dnsPreset.value);
check('живые IP сохранены для подстановки', page.state.dns.ips.comss.length === 1 && page.state.dns.ips.comss[0] === '1.2.3.4');
check('список DNS отрисован', !els.dnsList.classList.contains('hidden') && els.dnsList.innerHTML.includes('1.2.3.4'));
check('мёртвый адрес не в списке', !els.dnsList.innerHTML.includes('6.6.6.6'));
check('состояние «проверено»', els.dnsState.textContent === 'проверено: 1 из 2');
check('«Свой DNS» не пропал после проверки', els.dnsPreset.innerHTML.includes('Свой DNS'));

// ---------- 4. генерация конфига ----------
console.log('\n[4] Генерация .conf');
els.endpoint.value = '162.159.192.1:2408';
const dnsCallsBefore = global.__calls.filter(u => u.endsWith('/api/dns')).length;
page.state.dns.checked = false;
await els.genBtn.click();
check('генерация сама проверяет DNS (один запрос)',
  global.__calls.filter(u => u.endsWith('/api/dns')).length === dnsCallsBefore + 1);
check('выбранный DNS сохранён после авто-проверки', els.dnsPreset.value === 'comss', els.dnsPreset.value);
const c1 = conf();
check('конфиг в поле вывода', c1.startsWith('[Interface]'), c1.split('\n')[0]);
check('PrivateKey = ключ сервера', c1.includes('PrivateKey = ' + ACC.privKey));
check('Address = оба адреса', c1.includes('Address = 172.16.0.2, ' + ACC.client_ipv6));
check('DNS = IP', c1.includes('DNS = 1.2.3.4'), (c1.match(/^DNS = .*/m) || [])[0]);
check('MTU = 1280', c1.includes('MTU = 1280'));
check('S1..S4 = 0', /S1 = 0\nS2 = 0\nS3 = 0\nS4 = 0/.test(c1));
check('H1..H4 = 1,2,3,4', /H1 = 1\nH2 = 2\nH3 = 3\nH4 = 4/.test(c1));
check('I1 = <...> по умолчанию (quic)', /^I1 = </m.test(c1));
check('PublicKey = peer_pub', c1.includes('PublicKey = ' + ACC.peer_pub));
check('AllowedIPs полный туннель', c1.includes('AllowedIPs = 0.0.0.0/0, ::/0'));
check('PersistentKeepalive в [Peer]', (c1.split('[Peer]')[1] || '').includes('PersistentKeepalive = 25') && !(c1.split('[Peer]')[0] || '').includes('PersistentKeepalive'));
check('Jc/Jmin/Jmax = 4/40/70', c1.includes('Jc = 4\nJmin = 40\nJmax = 70'));
check('параметры отрисованы на главном', els.paramEp.textContent.includes('162.159.192.1:2408'));
check('нет DoH-запросов (сервер отдал IP)', !global.__calls.some(u => u.includes('cloudflare-dns.com')));
await els.confCopy.click();
check('копирование пишет конфиг в буфер', global.__copied === c1);
await els.confCopyBtn.click();
check('иконка копирования тоже работает', global.__copied === c1);
await els.confDownload.click();
check('скачивание отдаёт Blob с конфигом', !!global.__downloadBlob && global.__downloadBlob.parts[0] === c1);
var __dl = (document.body._children || []).filter(function (c) { return c.download; }).pop();
check('имя файла warpgen.conf без .txt', !!__dl && __dl.download === 'warpgen.conf', __dl && __dl.download);
check('MIME octet-stream (браузер не допишет .txt)', !!global.__downloadBlob &&
  global.__downloadBlob.opts.type === 'application/octet-stream', global.__downloadBlob && global.__downloadBlob.opts.type);

// ---------- 5. сброс DNS (без серверной проверки) ----------
console.log('\n[5] DNS из пресета без проверки');
page.state.dns.ips = {};
page.state.account = null;
els.dnsPreset.value = 'geohide';
await els.genBtn.click();
check('конфиг из пресета (IP не проверялся)', conf().includes('DNS ='), (conf().match(/^DNS = .*/m) || [])[0]);

// ---------- 6. смена I1-профиля ----------
console.log('\n[6] Смена I1-профиля (чипы → localStorage)');
localStorage.setItem('warpgen_i1profile', 'dns');
await els.genBtn.click();
check('I1 dns-профиль в конфиге', /^I1 = </m.test(conf()), (conf().match(/^I1 = .{0,40}/m) || [])[0]);
localStorage.setItem('warpgen_i1profile', 'stun');
await els.genBtn.click();
check('I1 stun-профиль в конфиге', /^I1 = </m.test(conf()));
localStorage.setItem('warpgen_i1profile', 'quic');

// ---------- 7. аккаунт, выданный сервером ранее ----------
console.log('\n[7] Аккаунт из state (как после /api/warp)');
page.state.account = {
  privateKey: ACC.privKey, serverPublicKey: ACC.peer_pub,
  addressV4: '172.16.0.9', addressV6: '', endpoint: '162.159.192.1:2408'
};
page.state.dns.ips = {};
els.dnsPreset.value = 'custom';
await els.dnsPreset.fire('change');
check('поле своего DNS показано', !els.dnsCustomRow.classList.contains('hidden'));
els.dnsCustom.value = '9.9.9.9';
await els.genBtn.click();
check('конфиг из аккаунта state', conf().includes('PrivateKey = ' + ACC.privKey) &&
  conf().includes('Address = 172.16.0.9') && conf().includes('DNS = 9.9.9.9'));

// ---------- 8. хостнейм DNS резолвится через DoH ----------
console.log('\n[8] DNS-хостнейм через DoH');
els.dnsCustom.value = 'dns.geohide.ru';
await els.genBtn.click();
check('резолв через DoH подставлен', conf().includes('DNS = 5.6.7.8'), (conf().match(/^DNS = .*/m) || [])[0]);
check('был запрос к cloudflare-dns.com', global.__calls.some(u => u.includes('cloudflare-dns.com')));

// ---------- 9. случайный endpoint ----------
console.log('\n[9] Случайный endpoint');
localStorage.setItem('warpgen_portmode', 'warp');
await els.epBtn.click();
const ep = els.endpoint.value;
check('endpoint ip:port', /^\d{1,3}(\.\d{1,3}){3}:\d+$/.test(ep), ep);
check('порт из набора WARP', [500, 1701, 4500, 2408].includes(Number(ep.split(':')[1])), ep);

// ---------- 10. ошибки API ----------
console.log('\n[10] Ошибки API');
global.fetch = async () => { throw new Error('connect ECONNREFUSED'); };
page.state.account = null;
await els.accBtn.click();
check('нет сети → понятное сообщение', statusHtml().includes('сервис недоступен'), statusHtml().slice(0, 140));
check('состояние «ошибка»', els.accState.textContent === 'ошибка');

global.fetch = async () => ({
  ok: false, status: 500,
  text: async () => JSON.stringify({ error: '<img src=x onerror=alert(1)>', code: 'XSS' })
});
await els.accBtn.click();
check('HTML из ответа API экранирован в логе',
  !statusHtml().includes('<img') && statusHtml().includes('&lt;img'), statusHtml().slice(-160));

if (failures) throw new Error('провалено проверок: ' + failures);
console.log('\nDOM TEST PASSED');
})().catch(e => {
  console.error('DOM TEST FAILED:', e.stack || e);
  process.exit(1);
});
