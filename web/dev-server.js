#!/usr/bin/env node
/**
 * dev-server.js — локальный эталон бэкенда для веб-генератора (вариант B).
 *
 * Только для разработки и ручной проверки: запускает статику из `web/` и два
 * эндпоинта, которые в продакшене должен отдавать Go-бинарник этого репозитория
 * (`registration` + `dnscheck`). Зависимостей нет, Node >= 18.
 *
 *   POST /api/warp  — регистрирует WARP-аккаунт в Cloudflare, возвращает
 *                     {privKey, peer_pub, client_ipv4, client_ipv6};
 *                     приватный ключ генерируется на сервере (как в ref1).
 *                     Тело — JSON `{}`, Content-Type: application/json:
 *                     так браузерная CSRF-защита не пропускает чужие формы.
 *   GET /api/dns    — пресеты DNS из config/dns_presets.go: резолв hostname'ов
 *                     и проверка живости UDP-запросом к самому резолверу
 *                     (не через туннель), с замером RTT.
 *
 * Запуск:
 *   node web/dev-server.js [--port 8080] [--host 127.0.0.1]
 *   make web-serve
 *
 * Секреты: `/api/warp` отдаёт приватный ключ клиента в браузер — так и задумано
 * (клиент собирает .conf). Сервер ничего не хранит: аккаунт живёт только в
 * памяти запроса.
 */
'use strict';

const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const dns = require('node:dns');
const dgram = require('node:dgram');

const REG_URL = 'https://api.cloudflareclient.com/v0a4005/reg';
const CF_CLIENT_VERSION = 'a-6.11-2223';
const CF_USER_AGENT = 'okhttp/3.12.1';

const QUERY_NAME = 'cloudflare.com';
const MAX_PROBE_IPS_PER_HOST = 4;
const DNS_TIMEOUT_MS = 3000;

// Копия config.DNSPresets (id/label/value) — сервер не должен тянуть Go-код.
const DNS_PRESETS = [
  { id: 'recommended', label: 'Рекомендуемые (Comss, Malw, GeoHide)', value: 'dns.comss.one,dns.malw.link,dns.geohide.ru' },
  { id: 'comss', label: 'Comss', value: 'dns.comss.one' },
  { id: 'malw', label: 'Malw', value: 'dns.malw.link' },
  { id: 'geohide', label: 'GeoHide', value: 'dns.geohide.ru' },
  { id: 'cloudflare', label: 'Cloudflare 1.1.1.1', value: '1.1.1.1' },
  { id: 'google', label: 'Google 8.8.8.8', value: '8.8.8.8' },
  { id: 'yandex', label: 'Яндекс.DNS 77.88.8.8', value: '77.88.8.8' }
];

// ---------- ключи WARP ----------
// X25519 средствами node:crypto: JWK отдаёт сырые ключи в base64url.
function generateKeypair() {
  const { privateKey, publicKey } = crypto.generateKeyPairSync('x25519');
  const priv = Buffer.from(privateKey.export({ format: 'jwk' }).d, 'base64url');
  const pub = Buffer.from(publicKey.export({ format: 'jwk' }).x, 'base64url');
  return { private: priv.toString('base64'), public: pub.toString('base64') };
}

// ---------- регистрация WARP (порт registration.Register) ----------
async function httpJSON(url, opts, fetchImpl) {
  const resp = await fetchImpl(url, opts);
  const text = await resp.text();
  let js = null;
  try { js = text ? JSON.parse(text) : null; } catch (e) { js = null; }
  if (!resp.ok) {
    const err = new Error(`статус ${resp.status}: ${text.slice(0, 300) || '(пусто)'}`);
    err.status = resp.status;
    err.body = js;
    throw err;
  }
  if (!js) throw new Error('ответ не JSON');
  return js;
}

function stripMask(addr) {
  return String(addr || '').split('/')[0];
}

async function registerAccount(deps) {
  const { fetchImpl } = deps;
  const keys = generateKeypair();

  const reg = await httpJSON(REG_URL, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'User-Agent': CF_USER_AGENT,
      'CF-Client-Version': CF_CLIENT_VERSION
    },
    body: JSON.stringify({ key: keys.public })
  }, fetchImpl);

  const peer = reg && reg.config && reg.config.peers && reg.config.peers[0];
  const addrs = (reg && reg.config && reg.config.interface && reg.config.interface.addresses) || {};
  if (!peer || !peer.public_key) throw Object.assign(new Error('в ответе регистрации нет публичного ключа пира'), { code: 'UPSTREAM' });
  if (!reg.id || !reg.token) throw Object.assign(new Error('в ответе регистрации нет id/token'), { code: 'UPSTREAM' });

  // Включаем WARP: без этого аккаунт не работает (как в registration.Register).
  await httpJSON(`${REG_URL}/${reg.id}`, {
    method: 'PATCH',
    headers: {
      'Content-Type': 'application/json',
      'User-Agent': CF_USER_AGENT,
      'CF-Client-Version': CF_CLIENT_VERSION,
      'Authorization': `Bearer ${reg.token}`
    },
    body: JSON.stringify({ warp_enabled: true })
  }, fetchImpl);

  return {
    privKey: keys.private,
    peer_pub: peer.public_key,
    client_ipv4: stripMask(addrs.v4),
    client_ipv6: stripMask(addrs.v6)
  };
}

// ---------- DNS: резолв + проверка живости ----------
function dnsQuery(name, id) {
  const parts = name.split('.');
  const labels = [];
  for (const p of parts) labels.push(Buffer.from(p, 'ascii'));
  const len = 12 + labels.reduce((s, l) => s + 1 + l.length, 0) + 1 + 4;
  const buf = Buffer.alloc(len);
  buf.writeUInt16BE(id, 0);
  buf.writeUInt16BE(0x0100, 2); // RD
  buf.writeUInt16BE(1, 4);      // QDCOUNT
  let off = 12;
  for (const l of labels) {
    buf[off++] = l.length;
    l.copy(buf, off);
    off += l.length;
  }
  buf[off++] = 0;
  buf.writeUInt16BE(1, off); off += 2; // QTYPE A
  buf.writeUInt16BE(1, off);            // QCLASS IN
  return buf;
}

// Проверяет резолвер: UDP-запрос A к самому серверу (не через туннель).
function probeDns(ip, opts) {
  const timeoutMs = (opts && opts.timeoutMs) || DNS_TIMEOUT_MS;
  const port = (opts && opts.port) || 53;
  return new Promise((resolve) => {
    const id = crypto.randomInt(0, 0xffff);
    const query = dnsQuery(QUERY_NAME, id);
    const udp = dgram.createSocket('udp4');
    const started = process.hrtime.bigint();
    let done = false;

    const finish = (res) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      try { udp.close(); } catch (e) { /* сокет уже закрыт */ }
      resolve(res);
    };

    const timer = setTimeout(() => finish({ ok: false, rttMs: null, error: 'таймаут' }), timeoutMs);

    udp.on('error', (e) => finish({ ok: false, rttMs: null, error: e.message }));
    udp.on('message', (msg) => {
      const rttMs = Number((process.hrtime.bigint() - started) / 1000000n);
      if (msg.length < 12) return finish({ ok: false, rttMs, error: 'короткий ответ' });
      const respId = msg.readUInt16BE(0);
      const flags = msg.readUInt16BE(2);
      const rcode = flags & 0x0f;
      const ancount = msg.readUInt16BE(6);
      if (respId !== id) return finish({ ok: false, rttMs, error: 'несовпадение id' });
      if (rcode !== 0) return finish({ ok: false, rttMs, error: 'rcode ' + rcode });
      if (ancount === 0) return finish({ ok: false, rttMs, error: 'ответ без адресов' });
      finish({ ok: true, rttMs: rttMs, error: null });
    });

    udp.send(query, 0, query.length, port, ip, (e) => {
      if (e) finish({ ok: false, rttMs: null, error: e.message });
    });
  });
}

async function resolveHost(host, deps) {
  const { resolve4 } = deps;
  if (/^[0-9.]+$/.test(host)) return [host];
  try {
    const ips = await resolve4(host);
    return Array.isArray(ips) ? ips : [];
  } catch (e) {
    return [];
  }
}

async function dnsReport(deps) {
  const out = [];
  for (const preset of DNS_PRESETS) {
    const values = preset.value.split(',').map((s) => s.trim()).filter(Boolean);
    const checks = [];
    for (const host of values) {
      const ips = (await resolveHost(host, deps)).slice(0, MAX_PROBE_IPS_PER_HOST);
      if (!ips.length) {
        checks.push({ host, ips: [], ok: false, rttMs: null, note: 'не резолвится', liveIps: [], deadIps: [] });
        continue;
      }
      // Проверяем до MAX_PROBE_IPS_PER_HOST адресов параллельно: у одного имени
      // часто несколько A-записей, и часть из них может не отвечать.
      const probes = await Promise.all(ips.map((ip) => deps.probe(ip).then(
        (r) => ({ ip, ...r }),
        (e) => ({ ip, ok: false, rttMs: null, error: String((e && e.message) || e) })
      )));
      const liveIps = probes.filter((r) => r.ok).sort((a, b) => a.rttMs - b.rttMs);
      const deadIps = probes.filter((r) => !r.ok).map((r) => r.ip);
      const best = liveIps[0];
      checks.push({
        host,
        ips: liveIps.map((r) => r.ip).concat(deadIps),
        liveIps: liveIps.map((r) => r.ip),
        deadIps,
        ok: Boolean(best),
        rttMs: best ? best.rttMs : null,
        note: best ? '' : (probes[0].error || 'нет ответа')
      });
    }
    const liveIps = checks.flatMap((c) => c.liveIps);
    const deadIps = checks.flatMap((c) => c.deadIps);
    out.push({
      id: preset.id,
      label: preset.label,
      host: values.join(', '),
      // Первыми идут отвечающие адреса: клиент подставляет первый в `DNS =`.
      ips: liveIps.concat(deadIps),
      ok: liveIps.length > 0,
      rttMs: checks.reduce((best, c) => (c.rttMs != null && (best == null || c.rttMs < best) ? c.rttMs : best), null),
      note: checks.filter((c) => !c.ok).map((c) => c.host + ': ' + c.note).join('; '),
      checks
    });
  }
  return out;
}

// ---------- HTTP ----------
const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.ico': 'image/x-icon',
  '.png': 'image/png',
  '.woff2': 'font/woff2',
  '.woff': 'font/woff'
};

function send(res, status, body, headers) {
  const h = Object.assign({
    'Cache-Control': 'no-store',
    'Access-Control-Allow-Origin': '*',
    'X-Content-Type-Options': 'nosniff'
  }, headers || {});
  res.writeHead(status, h);
  res.end(body);
}

function sendJSON(res, status, obj) {
  send(res, status, JSON.stringify(obj), { 'Content-Type': 'application/json; charset=utf-8' });
}

function createServer(opts) {
  const options = Object.assign({
    root: __dirname,
    fetchImpl: (...args) => fetch(...args),
    resolve4: (h) => dns.promises.resolve4(h),
    probe: (ip) => probeDns(ip),
    log: () => {}
  }, opts || {});

  // Простые лимиты: регистрации и проверки DNS в минуту на IP.
  // Ключи разные, чтобы активная проверка DNS не съедала лимит регистраций.
  const warpRate = new Map();
  const dnsRate = new Map();
  const LIMIT = options.registerLimit || 10;
  const DNS_LIMIT = options.dnsLimit || 30;

  function allowed(rate, ip, limit) {
    const now = Date.now();
    const hits = (rate.get(ip) || []).filter((t) => now - t < 60000);
    if (hits.length >= limit) return false;
    hits.push(now);
    rate.set(ip, hits);
    return true;
  }

  async function handle(req, res) {
    let url;
    try {
      url = new URL(req.url, 'http://localhost');
    } catch (e) {
      // Некорректный запрос не должен ронять процесс.
      return sendJSON(res, 400, { error: 'некорректный URL', code: 'BAD_REQUEST' });
    }
    const ip = req.socket.remoteAddress || 'unknown';
    options.log(req.method, url.pathname);

    // CORS-предполёт: страница может жить на другом хосте, чем API.
    if (req.method === 'OPTIONS') {
      return send(res, 204, '', {
        'Access-Control-Allow-Methods': 'GET, POST, OPTIONS',
        'Access-Control-Allow-Headers': 'Content-Type',
        'Access-Control-Max-Age': '600'
      });
    }

    if (url.pathname === '/api/warp') {
      // Тело не нужно, но его надо дочитать, иначе keep-alive-соединение
      // останется с непрочитанными байтами (даже при 405/415).
      req.resume();
      if (req.method !== 'POST') return sendJSON(res, 405, { error: 'только POST (тело {} и Content-Type: application/json)', code: 'METHOD_NOT_ALLOWED' });
      if (!/^application\/json\b/i.test(req.headers['content-type'] || '')) {
        return sendJSON(res, 415, { error: 'нужен Content-Type: application/json', code: 'UNSUPPORTED_MEDIA_TYPE' });
      }
      if (!allowed(warpRate, ip, LIMIT)) return sendJSON(res, 429, { error: 'слишком много запросов, подождите минуту', code: 'RATE_LIMITED' });
      try {
        const acc = await registerAccount(options);
        return sendJSON(res, 200, acc);
      } catch (e) {
        const st = e.status === 429 ? 429 : (e.status >= 400 && e.status < 500 ? 502 : 503);
        const code = e.status === 429 ? 'RATE_LIMITED'
          : e.code === 'UPSTREAM' ? 'UPSTREAM_UNAVAILABLE'
            : (e.name === 'AbortError' || /fetch failed|ENOTFOUND|EAI_AGAIN|timeout/i.test(String(e.message))) ? 'UPSTREAM_UNAVAILABLE'
              : 'REGISTRATION_FAILED';
        options.log('warp error:', e.message);
        return sendJSON(res, st, { error: 'не удалось зарегистрировать аккаунт: ' + e.message, code });
      }
    }

    if (url.pathname === '/api/dns') {
      if (req.method !== 'GET') return sendJSON(res, 405, { error: 'только GET', code: 'METHOD_NOT_ALLOWED' });
      if (!allowed(dnsRate, ip, DNS_LIMIT)) return sendJSON(res, 429, { error: 'слишком много проверок DNS, подождите минуту', code: 'RATE_LIMITED' });
      try {
        const presets = await dnsReport(options);
        return sendJSON(res, 200, { presets });
      } catch (e) {
        return sendJSON(res, 500, { error: 'проверка DNS не удалась: ' + e.message, code: 'DNS_CHECK_FAILED' });
      }
    }

    if (url.pathname === '/api/health') return sendJSON(res, 200, { ok: true, service: 'warpgen-web-dev' });

    // Статика.
    let rel;
    try {
      rel = decodeURIComponent(url.pathname);
    } catch (e) {
      // Битый percent-encoding (%/%) — это ошибка клиента, а не падение сервера.
      return sendJSON(res, 400, { error: 'некорректный путь', code: 'BAD_REQUEST' });
    }
    if (rel === '/' || rel === '') rel = '/index.html';
    const file = path.join(options.root, path.normalize(rel).replace(/^([/\\])+/, ''));
    if (!file.startsWith(options.root)) return send(res, 403, 'forbidden', { 'Content-Type': 'text/plain; charset=utf-8' });
    fs.readFile(file, (err, data) => {
      if (err) return send(res, 404, 'not found', { 'Content-Type': 'text/plain; charset=utf-8' });
      send(res, 200, data, { 'Content-Type': MIME[path.extname(file)] || 'application/octet-stream' });
    });
  }

  return http.createServer((req, res) => {
    // Ловим любые необработанные ошибки: один плохой запрос не должен
    // завершать весь дев-сервер (и оставлять висеть соединение).
    handle(req, res).catch((e) => {
      options.log('server error:', (e && e.message) || String(e));
      try {
        if (!res.headersSent) sendJSON(res, 500, { error: 'внутренняя ошибка сервера', code: 'INTERNAL' });
        else res.destroy();
      } catch (ignored) {
        try { res.destroy(); } catch (alsoIgnored) { /* соединение уже закрыто */ }
      }
    });
  });
}

function parseArgs(argv) {
  const out = { port: 8080, host: '127.0.0.1' };
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--port' && argv[i + 1]) out.port = Number(argv[++i]);
    else if (argv[i] === '--host' && argv[i + 1]) out.host = argv[++i];
  }
  return out;
}

if (require.main === module) {
  const args = parseArgs(process.argv.slice(2));
  const server = createServer({ log: (...a) => console.log('[dev]', ...a) });
  server.on('error', (err) => {
    if (err.code === 'EADDRINUSE') {
      // Понятное сообщение вместо трейса: чаще всего это забытый фоновый запуск
      // прошлой сессии. Не молчим и не падаем необработанным 'error'.
      console.error(`ошибка: порт ${args.host}:${args.port} занят (EADDRINUSE).`);
      console.error('  Возможно, сервер уже запущен — проверьте: curl http://' + args.host + ':' + args.port + '/api/health');
      console.error('  Либо остановите старый процесс: pkill -f dev-server.js\n  Либо другой порт: PORT=9000 make web-serve');
      process.exit(1);
    }
    console.error('ошибка сервера: ' + err.message);
    process.exit(1);
  });
  server.listen(args.port, args.host, () => {
    console.log(`warpgen web: http://${args.host}:${args.port}/`);
    console.log('  /api/warp — регистрация WARP (реальная сеть)');
    console.log('  /api/dns  — резолв и проверка DNS (реальная сеть)');
  });
}

module.exports = { createServer, registerAccount, dnsReport, probeDns, dnsQuery, generateKeypair, DNS_PRESETS };