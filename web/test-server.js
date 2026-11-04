// Тесты dev-server.js: сеть подменена фейками, UDP-проверка — на loopback.
// Запуск: node test-server.js
const crypto = require('crypto');
const dgram = require('node:dgram');
const fs = require('fs');
const path = require('path');

const srv = require('./dev-server.js');

let failures = 0;
function check(name, cond, extra) {
  console.log('   ', cond ? '✓' : '✗', name + (extra ? ' — ' + extra : ''));
  if (!cond) failures++;
}

// X25519 из warpgen.js, чтобы проверить, что dev-server считает тот же публичный ключ.
global.crypto = {
  getRandomValues(arr) { crypto.randomFillSync(Buffer.from(arr.buffer, arr.byteOffset, arr.byteLength)); return arr; },
  subtle: crypto.webcrypto.subtle
};
const W = new Function(fs.readFileSync(path.join(__dirname, 'warpgen.js'), 'utf8')
  .replace('  global.Warpgen = {', '  global.__x = { x25519Big: x25519Big },\n  global.Warpgen = {')
  + '; return { W: global.Warpgen, x: global.__x };')();

function fakeFetch(routes) {
  const calls = [];
  const impl = async (url, opts) => {
    calls.push({ url, opts });
    for (const r of routes) {
      if (url.includes(r.match)) {
        if (r.throw) throw new Error(r.throw);
        return {
          ok: r.status === undefined || r.status < 400,
          status: r.status || 200,
          text: async () => (typeof r.body === 'string' ? r.body : JSON.stringify(r.body))
        };
      }
    }
    throw new Error('fakeFetch: нет маршрута для ' + url);
  };
  impl.calls = calls;
  return impl;
}

const REG_OK = {
  id: 'acc-1', token: 'tok-1',
  config: {
    peers: [{ public_key: 'PEER_PUB=' }],
    interface: { addresses: { v4: '172.16.0.2/32', v6: '2606:4700:110::9/128' } }
  }
};

async function main() {
  // ---------- 1. ключи ----------
  console.log('[1] Ключи WARP');
  const kp = srv.generateKeypair();
  const priv = Buffer.from(kp.private, 'base64');
  const pub = Buffer.from(kp.public, 'base64');
  check('priv/pub по 32 байта', priv.length === 32 && pub.length === 32);
  check('clamp приватного ключа', (priv[0] & 7) === 0 && (priv[31] & 0x80) === 0 && (priv[31] & 0x40) !== 0);
  const basePoint = new Uint8Array(32); basePoint[0] = 9;
  const derived = Buffer.from(W.x.x25519Big(new Uint8Array(priv), basePoint));
  check('публичный ключ = X25519(priv, 9)', derived.equals(pub));

  // ---------- 2. регистрация ----------
  console.log('\n[2] Регистрация аккаунта (Cloudflare)');
  const f1 = fakeFetch([
    { match: '/v0a4005/reg/acc-1', body: {}, status: 200 },
    { match: '/v0a4005/reg', body: REG_OK, status: 200 }
  ]);
  const acc = await srv.registerAccount({ fetchImpl: f1 });
  const posted = JSON.parse(f1.calls[0].opts.body);
  check('POST /reg с публичным ключом', f1.calls[0].url.endsWith('/v0a4005/reg') &&
    f1.calls[0].opts.method === 'POST' && Buffer.from(posted.key, 'base64').length === 32);
  check('в теле только key (как в Go)', Object.keys(posted).length === 1 && typeof posted.key === 'string');
  check('заголовки CF-Client-Version/User-Agent',
    f1.calls[0].opts.headers['CF-Client-Version'] === 'a-6.11-2223' &&
    f1.calls[0].opts.headers['User-Agent'] === 'okhttp/3.12.1');
  check('PATCH /reg/<id> с warp_enabled и Bearer',
    f1.calls[1].url.endsWith('/v0a4005/reg/acc-1') && f1.calls[1].opts.method === 'PATCH' &&
    f1.calls[1].opts.headers.Authorization === 'Bearer tok-1' &&
    JSON.parse(f1.calls[1].opts.body).warp_enabled === true);
  check('контракт ответа ref1', acc.peer_pub === 'PEER_PUB=' && acc.client_ipv4 === '172.16.0.2' &&
    acc.client_ipv6 === '2606:4700:110::9', JSON.stringify({ v4: acc.client_ipv4, v6: acc.client_ipv6 }));
  check('маски /32 и /128 сняты', !acc.client_ipv4.includes('/') && !acc.client_ipv6.includes('/'));
  check('ключ из ответа — свой приватный', acc.privKey === kp.private || Buffer.from(acc.privKey, 'base64').length === 32);

  const f429 = fakeFetch([{ match: '/v0a4005/reg', status: 429, body: 'error code: 1015' }]);
  let e429 = null;
  try { await srv.registerAccount({ fetchImpl: f429 }); } catch (e) { e429 = e; }
  check('429 пробрасывается со статусом', e429 && e429.status === 429, e429 && e429.message.slice(0, 40));

  const fNoPeer = fakeFetch([{ match: '/v0a4005/reg', body: { id: 'x', token: 'y', config: {} } }]);
  let eNoPeer = null;
  try { await srv.registerAccount({ fetchImpl: fNoPeer }); } catch (e) { eNoPeer = e; }
  check('без peers → UPSTREAM', eNoPeer && eNoPeer.code === 'UPSTREAM', eNoPeer && eNoPeer.message);

  // ---------- 3. DNS-запрос ----------
  console.log('\n[3] Кодирование DNS-запроса');
  const q = srv.dnsQuery('cloudflare.com', 0x1234);
  check('id и флаги', q.readUInt16BE(0) === 0x1234 && q.readUInt16BE(2) === 0x0100);
  check('QDCOUNT=1, QTYPE=A, QCLASS=IN',
    q.readUInt16BE(4) === 1 && q.readUInt16BE(q.length - 4) === 1 && q.readUInt16BE(q.length - 2) === 1);
  check('имя закодировано метками', q[12] === 10 && q.slice(13, 23).toString('ascii') === 'cloudflare');

  // ---------- 4. проверка живости DNS на loopback ----------
  console.log('\n[4] Проверка живости DNS (локальный UDP)');
  const makeResponder = (handler) => new Promise((resolve) => {
    const s = dgram.createSocket('udp4');
    s.on('message', (msg, rinfo) => {
      const out = handler(msg);
      if (out) s.send(out, rinfo.port, rinfo.address);
    });
    s.bind(0, '127.0.0.1', () => resolve(s));
  });

  const good = await makeResponder((msg) => {
    const resp = Buffer.from(msg);
    resp.writeUInt16BE(0x8180, 2);   // QR + RD + RA
    resp.writeUInt16BE(1, 6);        // ANCOUNT
    return resp;
  });
  const r1 = await srv.probeDns('127.0.0.1', { port: good.address().port });
  check('живой резолвер → ok, rtt', r1.ok === true && typeof r1.rttMs === 'number', JSON.stringify(r1));
  good.close();

  const nx = await makeResponder((msg) => {
    const resp = Buffer.from(msg);
    resp.writeUInt16BE(0x8183, 2);   // rcode 3 (NXDOMAIN)
    return resp;
  });
  const r2 = await srv.probeDns('127.0.0.1', { port: nx.address().port });
  check('rcode 3 → не живой', r2.ok === false && /rcode 3/.test(r2.error), JSON.stringify(r2));
  nx.close();

  const empty = await makeResponder((msg) => {
    const resp = Buffer.from(msg);
    resp.writeUInt16BE(0x8180, 2);
    resp.writeUInt16BE(0, 6);        // ANCOUNT = 0
    return resp;
  });
  const r3 = await srv.probeDns('127.0.0.1', { port: empty.address().port });
  check('ответ без адресов → не живой', r3.ok === false && /без адресов/.test(r3.error), JSON.stringify(r3));
  empty.close();

  const silent = await makeResponder(() => null);
  const t0 = Date.now();
  const r4 = await srv.probeDns('127.0.0.1', { port: silent.address().port, timeoutMs: 150 });
  check('тишина → таймаут (по opts.timeoutMs)', r4.ok === false && /таймаут/.test(r4.error) && Date.now() - t0 < 1000);
  silent.close();

  // ---------- 5. отчёт по пресетам ----------
  console.log('\n[5] Отчёт /api/dns');
  const reported = await srv.dnsReport({
    resolve4: async (host) => (host.includes('comss') ? ['1.2.3.4'] : Promise.reject(new Error('нет'))),
    probe: async (ip) => ({ ok: ip === '1.2.3.4', rttMs: 21, error: ip === '1.2.3.4' ? null : 'таймаут' })
  });
  check('по пресету на каждый известный', reported.length === srv.DNS_PRESETS.length);
  const comss = reported.find(p => p.id === 'comss');
  const recommended = reported.find(p => p.id === 'recommended');
  check('живой пресет: ok + ip + rtt', comss.ok === true && comss.ips[0] === '1.2.3.4' && comss.rttMs === 21);
  const malw = reported.find(p => p.id === 'malw');
  // В группе достаточно одного живого адреса: пресет рекомендованных остаётся
  // рабочим, а мёртвые хосты только попадают в note.
  check('группа: жив один хост — пресет рабочий', recommended.ok === true &&
    recommended.ips[0] === '1.2.3.4' && /не резолвится/.test(recommended.note),
    recommended.note.slice(0, 60));
  check('полностью мёртвый пресет: ok=false и note', malw.ok === false &&
    malw.ips.length === 0 && /не резолвится/.test(malw.note), malw.note.slice(0, 60));
  check('у пресета есть плоский список ips', Array.isArray(comss.ips) && Array.isArray(recommended.ips));

  const multi = await srv.dnsReport({
    resolve4: async (host) => (host.includes('comss') ? ['9.9.9.9', '1.2.3.4', '8.8.4.4'] : Promise.reject(new Error('нет'))),
    probe: async (ip) => (ip === '1.2.3.4'
      ? { ok: true, rttMs: 30, error: null }
      : { ok: false, rttMs: null, error: 'таймаут' })
  });
  const m = multi.find(p => p.id === 'comss');
  check('живой адрес ищется среди нескольких A-записей', m.ok === true && m.rttMs === 30, JSON.stringify(m.ips));
  check('первым в списке — отвечающий адрес', m.ips[0] === '1.2.3.4', m.ips.join(','));
  check('мёртвые адреса остаются в списке (для показа)',
    m.ips.includes('9.9.9.9') && m.ips.includes('8.8.4.4'));

  // ---------- 6. HTTP-сервер ----------
  console.log('\n[6] HTTP-сервер: контракт API и статика');
  const server = srv.createServer({
    root: __dirname,
    registerLimit: 2,
    fetchImpl: fakeFetch([
      { match: '/v0a4005/reg/acc-1', body: {} },
      { match: '/v0a4005/reg', body: REG_OK }
    ]),
    resolve4: async () => ['1.2.3.4'],
    probe: async () => ({ ok: true, rttMs: 12, error: null })
  });
  await new Promise(r => server.listen(0, '127.0.0.1', r));
  const base = 'http://127.0.0.1:' + server.address().port;

  const warp = await fetch(base + '/api/warp');
  const warpJs = await warp.json();
  check('/api/warp 200 + JSON', warp.status === 200 && warpJs.peer_pub === 'PEER_PUB=');
  check('CORS и no-store', warp.headers.get('access-control-allow-origin') === '*' &&
    warp.headers.get('cache-control') === 'no-store');
  check('приватный ключ в ответе (клиент собирает .conf)',
    typeof warpJs.privKey === 'string' && Buffer.from(warpJs.privKey, 'base64').length === 32);

  const dns = await fetch(base + '/api/dns');
  const dnsJs = await dns.json();
  check('/api/dns 200 + список', dns.status === 200 && Array.isArray(dnsJs.presets) && dnsJs.presets.length > 0);
  check('каждый пресет с ips/ok/rttMs', dnsJs.presets.every(p => Array.isArray(p.ips) && 'ok' in p && 'rttMs' in p));

  const post = await fetch(base + '/api/warp', { method: 'POST' });
  check('POST /api/warp разрешён (ref2-стиль)', post.status === 200);
  const put = await fetch(base + '/api/warp', { method: 'PUT' });
  check('PUT → 405', put.status === 405, String(put.status));

  // лимит: 2 запроса на минуту из настроек; уже потратили 2 (GET+POST)
  const limited = await fetch(base + '/api/warp');
  const limitedJs = await limited.json();
  check('лимит запросов → 429 RATE_LIMITED',
    limited.status === 429 && limitedJs.code === 'RATE_LIMITED', JSON.stringify(limitedJs).slice(0, 80));

  const pre = await fetch(base + '/api/warp', { method: 'OPTIONS' });
  check('OPTIONS → 204 + CORS-заголовки', pre.status === 204 &&
    (pre.headers.get('access-control-allow-methods') || '').includes('POST'));

  const index = await fetch(base + '/');
  const indexText = await index.text();
  check('/ отдаёт index.html', index.status === 200 && indexText.includes('warpgen') &&
    indexText.includes('/api/warp') === false);
  check('content-type html', (index.headers.get('content-type') || '').includes('text/html'));
  const js = await fetch(base + '/warpgen.js');
  check('/warpgen.js отдаётся как JS', js.status === 200 &&
    (js.headers.get('content-type') || '').includes('javascript'));
  const trav = await fetch(base + '/../etc/passwd');
  check('выход за каталог закрыт', trav.status === 403 || trav.status === 404, String(trav.status));
  const missing = await fetch(base + '/nope.txt');
  check('нет файла → 404', missing.status === 404);

  server.close();

  // ---------- 7. сервер ломается предсказуемо ----------
  console.log('\n[7] Ошибки регистрации в HTTP-ответе');
  const server2 = srv.createServer({
    root: __dirname,
    fetchImpl: fakeFetch([{ match: '/v0a4005/reg', status: 429, body: 'error code: 1015' }])
  });
  await new Promise(r => server2.listen(0, '127.0.0.1', r));
  const base2 = 'http://127.0.0.1:' + server2.address().port;
  const rl = await fetch(base2 + '/api/warp');
  const rlJs = await rl.json();
  check('429 от Cloudflare → 429 RATE_LIMITED', rl.status === 429 && rlJs.code === 'RATE_LIMITED',
    JSON.stringify(rlJs).slice(0, 100));
  server2.close();

  const server3 = srv.createServer({
    root: __dirname,
    fetchImpl: async () => { throw new Error('fetch failed: ENOTFOUND api.cloudflareclient.com'); }
  });
  await new Promise(r => server3.listen(0, '127.0.0.1', r));
  const base3 = 'http://127.0.0.1:' + server3.address().port;
  const up = await fetch(base3 + '/api/warp');
  const upJs = await up.json();
  check('нет сети → UPSTREAM_UNAVAILABLE', up.status === 503 && upJs.code === 'UPSTREAM_UNAVAILABLE',
    JSON.stringify(upJs).slice(0, 100));
  server3.close();

  if (failures) throw new Error('провалено проверок: ' + failures);
  console.log('\nSERVER TEST PASSED');
}

main().catch(e => {
  console.error('SERVER TEST FAILED:', e.stack || e);
  process.exit(1);
});