/**
 * warpgen.js — библиотека веб-генератора WARP-safe `.conf` для AmneziaWG.
 *
 * Архитектура: аккаунт (ключи и адреса) выдаёт сервер (см. `API`), а `.conf`
 * собирается в браузере: I1-обфускация, junk-пакеты и инварианты проверяются
 * локально по правилам пакета `config` этого репозитория.
 *
 * Контракт сервера (реализации — любая, эталон для локальной проверки —
 * `web/dev-server.js`):
 *
 *   GET  <base>/api/warp
 *        200 {"privKey":"...","peer_pub":"...","client_ipv4":"172.16.0.2",
 *             "client_ipv6":"2606:4700:110:...","endpoint":"162.159.192.1:2408"}
 *        4xx/5xx {"error":"...","code":"RATE_LIMITED|UPSTREAM_UNAVAILABLE|..."}
 *
 *   GET  <base>/api/dns
 *        200 {"presets":[{"id":"comss","label":"Comss","host":"dns.comss.one",
 *             "ips":["1.2.3.4"],"ok":true,"rttMs":23}]}
 *
 * Инварианты (config.Generate): S1..S4 = 0, H1..H4 = 1,2,3,4, MTU = 1280,
 * PersistentKeepalive в [Peer], I1 — заглавное поле, DNS — только IP.
 */
(function (global) {
  'use strict';

  var crypto = global.crypto;
  if (!crypto || !crypto.getRandomValues) throw new Error('crypto.getRandomValues недоступен');
  var subtle = crypto.subtle;
  if (!subtle) throw new Error('crypto.subtle недоступен (нужен современный браузер)');

  var VERSION = '0.2.0';

  // ---------- утилиты ----------
  var b64chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
  function b64(bytes) {
    var out = '';
    for (var i = 0; i < bytes.length; i += 3) {
      var b1 = bytes[i], b2 = bytes[i + 1] || 0, b3 = bytes[i + 2] || 0;
      out += b64chars.charAt(b1 >> 2) + b64chars.charAt(((b1 & 3) << 4) | (b2 >> 4));
      if (i + 1 < bytes.length) {
        out += b64chars.charAt(((b2 & 15) << 2) | (b3 >> 6));
        out += (i + 2 < bytes.length) ? b64chars.charAt(b3 & 63) : '=';
      } else {
        out += '==';
      }
    }
    return out;
  }
  function randomBytes(n) {
    var out = new Uint8Array(n);
    crypto.getRandomValues(out);
    return out;
  }
  function randInt(lo, hi) { return lo + crypto.getRandomValues(new Uint32Array(1))[0] % (hi - lo + 1); }
  function pick(arr) { return arr[randInt(0, arr.length - 1)]; }

  // ---------- X25519 (RFC 7748, Montgomery ladder) ----------
  function x25519Big(scalar, point) {
    var P = (1n << 255n) - 19n;
    function fromBytes(u) {
      var v = 0n;
      for (var i = 31; i >= 0; i--) v = (v << 8n) | BigInt(u[i]);
      return v;
    }
    function toBytes(v) {
      var a = new Uint8Array(32);
      for (var i = 0; i < 32; i++) { a[i] = Number(v & 0xffn); v >>= 8n; }
      return a;
    }
    function mod(a) { a %= P; return a < 0n ? a + P : a; }
    function add(a, b) { return mod(a + b); }
    function sub(a, b) { return mod(a - b); }
    function mul(a, b) { return mod(a * b); }
    function inv(a) {
      var e = P - 2n, res = 1n, b = mod(a);
      while (e > 0n) {
        if ((e & 1n) === 1n) res = mul(res, b);
        b = mul(b, b);
        e >>= 1n;
      }
      return res;
    }
    var k = fromBytes(scalar);
    k &= ((1n << 255n) - 1n);
    k |= (1n << 254n);
    k &= ~7n;

    var x1 = mod(fromBytes(point));
    var x2 = 1n, z2 = 0n, x3 = x1, z3 = 1n, swap = 0n, tmp;
    for (var t = 254; t >= 0; t--) {
      var kt = (k >> BigInt(t)) & 1n;
      swap ^= kt;
      if (swap === 1n) { tmp = x2; x2 = x3; x3 = tmp; tmp = z2; z2 = z3; z3 = tmp; }
      swap = kt;
      var A = add(x2, z2), AA = mul(A, A);
      var B = sub(x2, z2), BB = mul(B, B);
      var E = sub(AA, BB);
      var C = add(x3, z3), D = sub(x3, z3);
      var DA = mul(D, A), CB = mul(C, B);
      var sum = add(DA, CB), dif = sub(DA, CB);
      x3 = mul(sum, sum);
      z3 = mul(x1, mul(dif, dif));
      x2 = mul(AA, BB);
      z2 = mul(E, add(AA, mul(121665n, E)));
    }
    if (swap === 1n) { tmp = x2; x2 = x3; x3 = tmp; tmp = z2; z2 = z3; z3 = tmp; }
    return toBytes(mul(x2, inv(z2)));
  }

  function generateKeypair() {
    var priv = randomBytes(32);
    priv[0] &= 248;
    priv[31] &= 127;
    priv[31] |= 64;
    var basepoint = new Uint8Array(32);
    basepoint[0] = 9;
    var pub = x25519Big(priv, basepoint);
    return { private: b64(priv), public: b64(pub) };
  }

  // ---------- I1: маскировка под протокол (порт config/i1.go) ----------
  var I1DefaultHosts = ['www.apple.com', 'www.google.com', 'www.microsoft.com', 'cdn.jsdelivr.net'];
  var i1StaticDefault = '<r 2><b 0x858000010001000000000669636c6f756403636f6d0000010001c00c000100010000105a00044d583737>';

  function i1RandomInt(lo, hi) { return randInt(lo, hi); }
  function i1RandomBytes(n) { return randomBytes(n); }

  async function i1HMAC(key, data) {
    var k = await subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
    var s = await subtle.sign('HMAC', k, data);
    return new Uint8Array(s);
  }

  // CPS chain builder (AmneziaWG: keep-байты + случайные вставки)
  function I1CpsChain() {
    var sb = '';
    return {
      bytes: function (b) {
        if (b.length > 0) {
          sb += '<b 0x' + Array.from(b).map(function (x) { return ('00' + x.toString(16)).slice(-2); }).join('') + '>';
        }
      },
      text: function (s) { this.bytes(new TextEncoder().encode(s)); },
      tag: function (name, n) { sb += '<' + name + ' ' + n + '>'; },
      String: function () { return sb; }
    };
  }

  var dnsPadMin = 60, dnsPadMax = 180, dnsLabelMax = 63;
  var stunSoftwareMin = 16, stunSoftwareMax = 32;

  function i1DnsName(host) {
    var out = [];
    var labels = host.replace(/^\./, '').split('.');
    for (var i = 0; i < labels.length; i++) {
      var label = labels[i];
      if (label.length === 0 || label.length > dnsLabelMax) throw new Error('некорректная DNS-метка "' + label + '" в "' + host + '"');
      out.push(label.length);
      for (var j = 0; j < label.length; j++) out.push(label.charCodeAt(j));
    }
    out.push(0);
    return out;
  }

  async function i1GenDNS(host) {
    var question = i1DnsName(host).concat([0, 1, 0, 1]);
    var padLen = i1RandomInt(dnsPadMin, dnsPadMax);
    var opt = [
      0,
      (41 >> 8) & 0xff, 41 & 0xff,
      (4096 >> 8) & 0xff, 4096 & 0xff,
      0, 0, 0, 0,
      ((padLen + 4) >> 8) & 0xff, (padLen + 4) & 0xff,
      (12 >> 8) & 0xff, 12 & 0xff,
      (padLen >> 8) & 0xff, padLen & 0xff
    ];
    var c = I1CpsChain();
    c.tag('r', 2);
    c.bytes(new Uint8Array([1, 0, 0, 1, 0, 0, 0, 0, 0, 1]));
    c.bytes(new Uint8Array(question));
    c.bytes(new Uint8Array(opt));
    c.tag('r', padLen);
    return c.String();
  }

  async function i1GenSIP(host) {
    var c = I1CpsChain();
    c.text('OPTIONS sip:' + host + ' SIP/2.0\r\n');
    c.text('Via: SIP/2.0/UDP 192.168.' + i1RandomInt(0, 255) + '.' + i1RandomInt(2, 254) + ':5060;branch=z9hG4bK\r\n');
    c.tag('rc', 10);
    c.text('\r\nFrom: <sip:');
    c.tag('rc', 8);
    c.text('@' + host + '>;tag=');
    c.tag('rd', 6);
    c.text('\r\nTo: <sip:' + host + '>\r\nCall-ID: ');
    c.tag('rc', 16);
    c.text('@' + host + '\r\nCSeq: ');
    c.tag('rd', 4);
    c.text(' OPTIONS\r\nMax-Forwards: 70\r\nUser-Agent: PJSIP/2.13\r\nContent-Length: 0\r\n\r\n');
    return c.String();
  }

  async function i1GenSTUN(host) {
    var swlen = i1RandomInt(stunSoftwareMin >> 2, stunSoftwareMax >> 2) * 4;
    var c = I1CpsChain();
    c.bytes(new Uint8Array([0x00, 0x01]));
    var lenb = new Uint8Array(2);
    lenb[0] = ((swlen + 4) >> 8) & 0xff;
    lenb[1] = (swlen + 4) & 0xff;
    c.bytes(lenb);
    c.bytes(new Uint8Array([0x21, 0x12, 0xa4, 0x42]));
    c.tag('r', 12);
    var soft = new Uint8Array(4);
    soft[0] = 0x80; soft[1] = 0x22;
    soft[2] = (swlen >> 8) & 0xff; soft[3] = swlen & 0xff;
    c.bytes(soft);
    c.tag('rc', swlen);
    return c.String();
  }

  // --- QUIC Initial (RFC 9001) ---
  var quicInitialSalt = new Uint8Array([
    0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17,
    0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a
  ]);

  function quicConcat(arrs) {
    var len = 0, i;
    for (i = 0; i < arrs.length; i++) len += arrs[i].length;
    var out = new Uint8Array(len), off = 0;
    for (i = 0; i < arrs.length; i++) { out.set(arrs[i], off); off += arrs[i].length; }
    return out;
  }

  function quicStr8(b) {
    var out = new Uint8Array(1 + b.length);
    out[0] = b.length;
    out.set(b, 1);
    return out;
  }

  // HKDF-Expand-Label: info = uint16(length) || str8("tls13 "+label) || 0x00 0x01
  async function quicExpandLabel(secret, label, length) {
    var full = new TextEncoder().encode('tls13 ' + label);
    var info = quicConcat([
      new Uint8Array([(length >> 8) & 0xff, length & 0xff]),
      quicStr8(full),
      new Uint8Array([0, 1])
    ]);
    var hm = await i1HMAC(secret, info);
    return hm.slice(0, length);
  }

  async function quicInitialKeys(dcid) {
    var initial = await i1HMAC(quicInitialSalt, dcid);
    var client = await quicExpandLabel(initial, 'client in', 32);
    return {
      key: await quicExpandLabel(client, 'quic key', 16),
      iv: await quicExpandLabel(client, 'quic iv', 12),
      hp: await quicExpandLabel(client, 'quic hp', 16)
    };
  }

  async function quicHeaderMask(hp, sample) {
    var key = await subtle.importKey('raw', hp, { name: 'AES-CBC' }, false, ['encrypt']);
    // AES-CBC с нулевым IV на одном блоке == AES-ECB (block.Encrypt в Go)
    var m = await subtle.encrypt({ name: 'AES-CBC', iv: new Uint8Array(16) }, key, sample.slice(0, 16));
    return new Uint8Array(m).slice(0, 16);
  }

  function quicVarint(x) {
    if (x < 0x40) return new Uint8Array([x]);
    if (x < 0x4000) {
      var b = new Uint8Array(2);
      b[0] = ((x >> 8) | 0x40) & 0xff; b[1] = x & 0xff;
      return b;
    }
    var b4 = new Uint8Array(4);
    b4[0] = ((x >> 24) | 0x80) & 0xff; b4[1] = (x >> 16) & 0xff; b4[2] = (x >> 8) & 0xff; b4[3] = x & 0xff;
    return b4;
  }

  var quicHelloRandLen = 32, quicSampleEnd = 20, tlsHandshakeHead = 6;

  function tlsClientHello(sni) {
    var name = new TextEncoder().encode(sni);
    var serverName = new Uint8Array(2 + 1 + 2 + name.length);
    serverName[0] = ((3 + name.length) >> 8) & 0xff;
    serverName[1] = (3 + name.length) & 0xff;
    serverName[2] = 0;
    serverName[3] = (name.length >> 8) & 0xff;
    serverName[4] = name.length & 0xff;
    serverName.set(name, 5);

    var ext = new Uint8Array(2 + 2 + serverName.length);
    ext[0] = 0; ext[1] = 0;
    ext[2] = (serverName.length >> 8) & 0xff; ext[3] = serverName.length & 0xff;
    ext.set(serverName, 4);

    var body = new Uint8Array(2 + 32 + 4 + 2 + ext.length);
    body[0] = 0x03; body[1] = 0x03;
    body.set(i1RandomBytes(32), 2);
    body[38] = (ext.length >> 8) & 0xff;
    body[39] = ext.length & 0xff;
    body.set(ext, 40);

    var hello = new Uint8Array(4 + body.length);
    hello[0] = 0x01;
    hello[1] = (body.length >> 16) & 0xff;
    hello[2] = (body.length >> 8) & 0xff;
    hello[3] = body.length & 0xff;
    hello.set(body, 4);
    return hello;
  }

  function quicCryptoFrame(data) {
    return quicConcat([new Uint8Array([0x06]), quicVarint(0), quicVarint(data.length), data]);
  }

  async function quicInitialPacket(dcid, pkn, payload) {
    var header = quicConcat([
      new Uint8Array([0xC0 | (pkn.length - 1)]),
      new Uint8Array([0, 0, 0, 1]),
      quicStr8(dcid),
      new Uint8Array([0, 0]),
      quicVarint(pkn.length + payload.length + 16),
      pkn
    ]);
    var headerLen = header.length;

    var keys = await quicInitialKeys(dcid);
    var iv = new Uint8Array(keys.iv);
    for (var i = 0; i < pkn.length; i++) iv[iv.length - pkn.length + i] ^= pkn[i];

    var key = await subtle.importKey('raw', keys.key, { name: 'AES-GCM' }, false, ['encrypt']);
    var ciphertext = new Uint8Array(await subtle.encrypt({
      name: 'AES-GCM', iv: iv, additionalData: header
    }, key, payload));

    var sample = ciphertext.slice(4 - pkn.length, quicSampleEnd - pkn.length);
    var mask = await quicHeaderMask(keys.hp, sample);
    var masked = new Uint8Array(header);
    masked[0] ^= mask[0] & 0x0f;
    for (var j = 0; j < pkn.length; j++) masked[headerLen - pkn.length + j] ^= mask[1 + j];

    return { packet: quicConcat([masked, ciphertext]), headerLen: headerLen };
  }

  function quicCutParts(frameHeaderLen, helloLen, pknLen, headerLen) {
    var keep = frameHeaderLen + tlsHandshakeHead;
    var randomized = quicHelloRandLen;
    var short = quicSampleEnd - pknLen - keep;
    if (short > 0) { keep += short; randomized -= short; }
    return [headerLen + keep, randomized, helloLen - tlsHandshakeHead - quicHelloRandLen, 16];
  }

  function quicToCps(packet, parts) {
    var c = I1CpsChain();
    var offset = 0, keep = true;
    for (var k = 0; k < parts.length; k++) {
      var n = parts[k];
      if (n > 0) {
        if (keep) c.bytes(packet.slice(offset, offset + n));
        else c.tag('r', n);
      }
      offset += n;
      keep = !keep;
    }
    return c.String();
  }

  async function quicGenI1(sni) {
    var dcid = i1RandomBytes(1);
    var pkn = new Uint8Array([0]);
    var hello = tlsClientHello(sni);
    var payload = quicCryptoFrame(hello);
    var pkt = await quicInitialPacket(dcid, pkn, payload);
    var parts = quicCutParts(payload.length - hello.length, hello.length, pkn.length, pkt.headerLen);
    return quicToCps(pkt.packet, parts);
  }

  // ---------- I1-генератор ----------
  var i1Generators = { quic: quicGenI1, dns: i1GenDNS, sip: i1GenSIP, stun: i1GenSTUN };

  var i1Generator = (function () {
    function profiles() { return ['quic', 'dns', 'sip', 'stun']; }
    async function generate(profile, host) {
      if (!profile || profile === 'random') profile = pick(profiles());
      if (!host) host = pick(I1DefaultHosts);
      var gen = i1Generators[profile];
      if (!gen) throw new Error('неизвестный профиль I1 "' + profile + '"');
      var chain = await gen(host);
      return { chain: chain, label: profile + '(' + host + ')' };
    }
    function defaults() { return i1StaticDefault; }
    return { generate: generate, profiles: profiles, defaults: defaults };
  })();

  // ---------- пресеты ----------
  var WARP_SAFE = {
    S1: 0, S2: 0, S3: 0, S4: 0,
    H1: 1, H2: 2, H3: 3, H4: 4,
    MTU: 1280, PersistentKeepalive: 25,
    AllowedIPs: ['0.0.0.0/0', '::/0']
  };

  var LIMITS = {
    mtu: { min: 1280, max: 1420, def: 1280 },
    keepalive: { min: 10, max: 120, def: 25 },
    jc: { min: 1, max: 128, def: 4 },
    jmin: { min: 1, max: 1279, def: 40 },
    jmax: { min: 2, max: 1280, def: 70 }
  };

  // Значения совпадают с config/dns_presets.go.
  var DNSPresets = [
    { id: 'recommended', label: 'Рекомендуемые (Comss, Malw, GeoHide)', value: 'dns.comss.one, dns.malw.link, dns.geohide.ru', def: true },
    { id: 'comss', label: 'Comss', value: 'dns.comss.one' },
    { id: 'malw', label: 'Malw', value: 'dns.malw.link' },
    { id: 'geohide', label: 'GeoHide', value: 'dns.geohide.ru' },
    { id: 'cloudflare', label: 'Cloudflare 1.1.1.1', value: '1.1.1.1' },
    { id: 'google', label: 'Google 8.8.8.8', value: '8.8.8.8' },
    { id: 'yandex', label: 'Яндекс.DNS 77.88.8.8', value: '77.88.8.8' },
    { id: 'custom', label: 'Свой DNS', value: '' }
  ];

  // junk-пакеты: наш Go-дефолт + варианты из публичных генераторов.
  var JunkPresets = [
    { id: 'warpgen', label: 'warpgen (Jc 4 / 40 / 70)', jc: 4, jmin: 40, jmax: 70, def: true },
    { id: 'compatible', label: 'compatible (4 / 64 / 96)', jc: 4, jmin: 64, jmax: 96 },
    { id: 'balanced', label: 'balanced (6 / 64 / 160)', jc: 6, jmin: 64, jmax: 160 },
    { id: 'enhanced', label: 'enhanced (10 / 96 / 320)', jc: 10, jmin: 96, jmax: 320 },
    { id: 'random', label: 'случайно', random: true },
    { id: 'custom', label: 'свои значения', custom: true }
  ];

  // WARP: диапазоны scanner/, порты из scanner.DefaultPorts; совместимы с ref1.
  var EndpointPresets = {
    prefixes: [
      '162.159.192', '162.159.193',
      '162.159.195',
      '188.114.96', '188.114.97', '188.114.98', '188.114.99'
    ],
    ports: [500, 1701, 4500, 2408],
    // Широкий набор портов: WARP слушает их на тех же адресах.
    widePorts: [500, 854, 859, 864, 878, 880, 890, 891, 894, 903, 908, 928, 934, 939, 942, 943,
      945, 946, 955, 968, 987, 988, 1002, 1010, 1014, 1018, 1070, 1074, 1180, 1387, 1701, 1843,
      2371, 2408, 2506, 3138, 3476, 3581, 3854, 4177, 4198, 4233, 4500, 5279, 5956, 7103, 7152,
      7156, 7281, 7559, 8319, 8742, 8854, 8886],
    defaultPort: 2408
  };

  function randomEndpoint(opts) {
    opts = opts || {};
    var port = opts.port && Number(opts.port) > 0
      ? Number(opts.port)
      : pick(opts.widePorts ? EndpointPresets.widePorts : EndpointPresets.ports);
    var prefix = pick(EndpointPresets.prefixes);
    var host = prefix + '.' + randInt(1, 254);
    return host + ':' + port;
  }

  // ---------- HTTP-клиент сервера ----------
  var API = (function () {
    var base = '';

    function configure(b) {
      if (typeof b === 'string') base = b.replace(/\/+$/, '');
      return base;
    }

    function baseUrl() {
      if (base) return base;
      if (typeof location !== 'undefined' && location.origin && location.protocol !== 'file:') return location.origin;
      return '';
    }

    function apiError(code, message, payload) {
      var e = new Error(message || code);
      e.code = code;
      e.payload = payload;
      return e;
    }

    async function request(path, opts) {
      opts = opts || {};
      var url = baseUrl() + path;
      var controller = typeof AbortController !== 'undefined' ? new AbortController() : null;
      var timer = controller ? setTimeout(function () { controller.abort(); }, opts.timeout || 25000) : null;
      var resp;
      try {
        resp = await fetch(url, {
          method: 'GET',
          headers: { 'Accept': 'application/json' },
          signal: controller ? controller.signal : undefined,
          cache: 'no-store'
        });
      } catch (e) {
        if (timer) clearTimeout(timer);
        throw apiError('NETWORK_ERROR', 'Сервер недоступен: ' + ((e && e.message) || e));
      }
      if (timer) clearTimeout(timer);

      var text = '';
      try { text = await resp.text(); } catch (e) { /* тело не дочиталось */ }
      var js = null;
      try { js = text ? JSON.parse(text) : null; } catch (e) { js = null; }

      if (!resp.ok) {
        var code = (js && (js.code || js.error)) || ('HTTP_' + resp.status);
        var msg = (js && (js.message || js.error)) || ('сервер ответил ' + resp.status);
        throw apiError(String(code), msg, js);
      }
      if (!js) throw apiError('BAD_RESPONSE', 'сервер вернул не JSON');
      return js;
    }

    // Аккаунт WARP: ключи и адреса выдаёт сервер.
    async function account() {
      var js = await request('/api/warp');
      var a = js.registration || js;
      var acc = {
        privateKey: a.privKey || a.privateKey || '',
        serverPublicKey: a.peer_pub || a.peerPublicKey || '',
        addressV4: a.client_ipv4 || a.ipv4 || '',
        addressV6: a.client_ipv6 || a.ipv6 || '',
        endpoint: a.endpoint || ''
      };
      var missing = [];
      if (!acc.privateKey) missing.push('privKey');
      if (!acc.serverPublicKey) missing.push('peer_pub');
      if (!acc.addressV4 && !acc.addressV6) missing.push('client_ipv4/client_ipv6');
      if (missing.length) {
        throw apiError('BAD_RESPONSE', 'сервер не вернул: ' + missing.join(', '), js);
      }
      return acc;
    }

    // Пресеты DNS с уже разрешёнными IP и признаком живости.
    async function dns() {
      var js = await request('/api/dns', { timeout: 20000 });
      var list = js.presets || js.dns || [];
      if (!Array.isArray(list)) throw apiError('BAD_RESPONSE', 'сервер вернул не список DNS');
      return list.map(function (p) {
        var live = Array.isArray(p.liveIps) ? p.liveIps : null;
        var ips = Array.isArray(p.ips) ? p.ips : (p.ip ? [p.ip] : []);
        // liveIps (если сервер их отдал) — адреса, ответившие на пробу;
        // остальные остаются только для показа.
        if (live && live.length) ips = live.concat(ips.filter(function (ip) { return live.indexOf(ip) < 0; }));
        return {
          id: p.id || p.name || '',
          label: p.label || p.name || p.id || '',
          host: p.host || p.value || '',
          ips: ips,
          liveIps: live && live.length ? live.slice() : [],
          ok: p.ok !== false && (p.ok === true || p.rttMs > 0),
          rttMs: typeof p.rttMs === 'number' ? p.rttMs : null,
          note: p.note || ''
        };
      });
    }

    return { configure: configure, baseUrl: baseUrl, account: account, dns: dns, request: request };
  })();

  // ---------- сборка .conf ----------
  var CONFIG = (function () {
    function newConfig() {
      return {
        privateKey: '', serverPublicKey: '', endpoint: '',
        addressV4: '', addressV6: '', dns: '',
        mtu: WARP_SAFE.MTU, keepalive: WARP_SAFE.PersistentKeepalive,
        jc: LIMITS.jc.def, jmin: LIMITS.jmin.def, jmax: LIMITS.jmax.def,
        i1: null, i1Label: '',
        warnings: [], dnsErrors: [],
        addresses: WARP_SAFE.AllowedIPs.slice(),
        S1: WARP_SAFE.S1, S2: WARP_SAFE.S2, S3: WARP_SAFE.S3, S4: WARP_SAFE.S4,
        H1: WARP_SAFE.H1, H2: WARP_SAFE.H2, H3: WARP_SAFE.H3, H4: WARP_SAFE.H4
      };
    }

    function isIP(s) {
      return /^(?:\d{1,3}\.){3}\d{1,3}$/.test(s) || (s.indexOf(':') >= 0 && /^[0-9a-fA-F:.]+$/.test(s));
    }

    function isEndpoint(s) {
      // host:port, где host — IPv4 или имя; порт 1..65535
      var m = /^([A-Za-z0-9.:_-]+):(\d{1,5})$/.exec(String(s || '').trim());
      if (!m) return false;
      var port = Number(m[2]);
      return port >= 1 && port <= 65535;
    }

    // Валидация I1-цепочки AmneziaWG: только <b 0x..>, <r n>, <rc n>, <rd n>.
    function validateI1(chain) {
      if (typeof chain !== 'string' || chain.length === 0) return false;
      var re = /^((<b 0x[0-9a-fA-F]+>)|(<r \d+>)|(<rc \d+>)|(<rd \d+>))+$|^(<r 0>)$/;
      return re.test(chain.trim());
    }

    // Резолв через DNS-over-HTTPS — запасной путь, когда сервер не отдал IP.
    async function resolveDoh(hostname) {
      var url = 'https://cloudflare-dns.com/dns-query?name=' + encodeURIComponent(hostname) + '&type=A';
      var resp = await fetch(url, { headers: { accept: 'application/dns-json' }, cache: 'no-store' });
      if (!resp.ok) throw new Error('DoH HTTP ' + resp.status);
      var js = await resp.json();
      var answers = (js.Answer || []).filter(function (a) { return a.type === 1 && a.data; });
      if (!answers.length) throw new Error('нет A-записи');
      return answers[0].data;
    }

    function junkValues(input) {
      if (input && typeof input.junk === 'object' && input.junk) {
        var j = input.junk;
        return { jc: Number(j.jc), jmin: Number(j.jmin), jmax: Number(j.jmax) };
      }
      if (input && input.junk === 'random') {
        var jmin = randInt(10, 50);
        return { jc: randInt(4, 6), jmin: jmin, jmax: jmin + randInt(20, 100) };
      }
      if (input && typeof input.junk === 'string') {
        for (var i = 0; i < JunkPresets.length; i++) {
          if (JunkPresets[i].id === input.junk && JunkPresets[i].jc) {
            var p = JunkPresets[i];
            return { jc: p.jc, jmin: p.jmin, jmax: p.jmax };
          }
        }
      }
      return null;
    }

    async function build(input) {
      input = input || {};
      var c = newConfig();

      var acc = input.account || {};
      c.privateKey = (acc.privateKey || input.privateKey || '').trim();
      c.serverPublicKey = (acc.serverPublicKey || input.serverPublicKey || '').trim();
      c.addressV4 = (acc.addressV4 || input.addressV4 || '').trim();
      c.addressV6 = (acc.addressV6 || input.addressV6 || '').trim();

      c.endpoint = (input.endpoint || acc.endpoint || '').trim();

      // DNS: список IP/hostname; hostname'ы резолвим (сервер обычно отдаёт IP).
      var rawDns = input.dns;
      if (Array.isArray(rawDns)) rawDns = rawDns.join(', ');
      if (rawDns) {
        var parts = String(rawDns).split(',').map(function (x) { return x.trim(); }).filter(Boolean);
        var resolved = [];
        for (var i = 0; i < parts.length; i++) {
          var p = parts[i];
          if (isIP(p)) { resolved.push(p); continue; }
          try { resolved.push(await resolveDoh(p)); }
          catch (e) { c.dnsErrors.push('DNS ' + p + ': ' + ((e && e.message) || e)); }
        }
        c.dns = resolved.join(', ');
      }

      if (input.mtu !== undefined && input.mtu !== null && input.mtu !== '') {
        c.mtu = parseInt(input.mtu, 10);
      }
      if (input.keepalive !== undefined && input.keepalive !== null && input.keepalive !== '') {
        c.keepalive = parseInt(input.keepalive, 10);
      }

      var junk = junkValues(input);
      if (junk) { c.jc = junk.jc; c.jmin = junk.jmin; c.jmax = junk.jmax; }

      if (input.i1) {
        c.i1 = String(input.i1).trim();
        c.i1Label = 'своя цепочка';
      } else {
        var profile = input.i1Profile || 'quic';
        if (profile === 'none') {
          c.i1 = '';
          c.i1Label = 'без I1';
        } else {
          c.i1 = i1StaticDefault;
          c.i1Label = profile;
          try {
            var res = await i1Generator.generate(profile, input.i1Host);
            c.i1 = res.chain;
            c.i1Label = res.label;
          } catch (e) {
            c.i1 = i1StaticDefault;
            c.i1Label = 'статический DNS-шаблон (' + ((e && e.message) || e) + ')';
            c.warnings.push('I1: ' + ((e && e.message) || e) + ' — подставлен статический шаблон');
          }
        }
      }
      return c;
    }

    function addressLine(c) {
      if (c.addressV4 && c.addressV6) return c.addressV4 + ', ' + c.addressV6;
      if (c.addressV4) return c.addressV4;
      return c.addressV6;
    }

    function errors(c) {
      var errs = [];
      if (!c.privateKey) errs.push('PrivateKey не задан (получите аккаунт)');
      if (!c.serverPublicKey) errs.push('ServerPublicKey не задан (получите аккаунт)');
      if (!c.endpoint) errs.push('Endpoint не задан');
      else if (!isEndpoint(c.endpoint)) errs.push('Endpoint «' + c.endpoint + '» должен быть в виде host:port');
      if (!c.addressV4 && !c.addressV6) errs.push('Address не задан');
      if (c.S1 !== 0 || c.S2 !== 0 || c.S3 !== 0 || c.S4 !== 0) errs.push('S1..S4 должны быть 0');
      if (c.H1 !== 1 || c.H2 !== 2 || c.H3 !== 3 || c.H4 !== 4) errs.push('H1..H4 должны быть 1,2,3,4');

      if (!Number.isFinite(c.mtu) || c.mtu < LIMITS.mtu.min || c.mtu > LIMITS.mtu.max) {
        errs.push('MTU должен быть ' + LIMITS.mtu.min + '..' + LIMITS.mtu.max);
      }
      if (!Number.isFinite(c.keepalive) || c.keepalive < LIMITS.keepalive.min || c.keepalive > LIMITS.keepalive.max) {
        errs.push('PersistentKeepalive должен быть ' + LIMITS.keepalive.min + '..' + LIMITS.keepalive.max);
      }
      if (!Number.isFinite(c.jc) || c.jc < LIMITS.jc.min || c.jc > LIMITS.jc.max) {
        errs.push('Jc должен быть ' + LIMITS.jc.min + '..' + LIMITS.jc.max);
      }
      if (!Number.isFinite(c.jmin) || c.jmin < LIMITS.jmin.min || c.jmin > LIMITS.jmin.max) {
        errs.push('Jmin должен быть ' + LIMITS.jmin.min + '..' + LIMITS.jmin.max);
      }
      if (!Number.isFinite(c.jmax) || c.jmax <= c.jmin || c.jmax > LIMITS.jmax.max) {
        errs.push('Jmax должен быть больше Jmin и не больше ' + LIMITS.jmax.max);
      }

      if (c.dns) {
        var parts = c.dns.split(',');
        for (var i = 0; i < parts.length; i++) {
          var p = parts[i].trim();
          if (p && !isIP(p)) errs.push('DNS «' + p + '» не является IP');
        }
      }
      if (c.i1 && !validateI1(c.i1)) {
        errs.push('I1 — некорректная цепочка (разрешены только <b 0x..>, <r n>, <rc n>, <rd n>)');
      }
      for (var k = 0; k < c.dnsErrors.length; k++) errs.push(c.dnsErrors[k]);
      return errs;
    }

    // Собирает .conf из готового конфига (`build`) или сразу из входных данных:
    // generate(input) === generate(await build(input)), чтобы вызов нельзя было
    // испортить забытым build.
    async function generate(input) {
      var c = (input && Array.isArray(input.dnsErrors)) ? input : await build(input);
      return renderConf(c);
    }

    function renderConf(c) {
      var e = errors(c);
      if (e.length) return { ok: false, errors: e, warnings: c.warnings || [], conf: null };

      var lines = [];
      lines.push('[Interface]');
      lines.push('PrivateKey = ' + c.privateKey);
      lines.push('Address = ' + addressLine(c));
      if (c.dns) lines.push('DNS = ' + c.dns);
      lines.push('MTU = ' + c.mtu);
      lines.push('S1 = ' + c.S1);
      lines.push('S2 = ' + c.S2);
      lines.push('S3 = ' + c.S3);
      lines.push('S4 = ' + c.S4);
      lines.push('Jc = ' + c.jc);
      lines.push('Jmin = ' + c.jmin);
      lines.push('Jmax = ' + c.jmax);
      lines.push('H1 = ' + c.H1);
      lines.push('H2 = ' + c.H2);
      lines.push('H3 = ' + c.H3);
      lines.push('H4 = ' + c.H4);
      if (c.i1) lines.push('I1 = ' + c.i1);
      lines.push('');
      lines.push('[Peer]');
      lines.push('PublicKey = ' + c.serverPublicKey);
      lines.push('AllowedIPs = ' + c.addresses.join(', '));
      lines.push('Endpoint = ' + c.endpoint);
      lines.push('PersistentKeepalive = ' + c.keepalive);

      return { ok: true, errors: [], warnings: c.warnings || [], conf: lines.join('\n') + '\n' };
    }

    return {
      build: build,
      generate: generate,
      render: renderConf,
      errors: errors,
      validateI1: validateI1,
      addressLine: addressLine,
      isIP: isIP,
      presets: { dns: DNSPresets, junk: JunkPresets }
    };
  })();

  global.Warpgen = {
    version: VERSION,
    generateKeypair: generateKeypair,
    i1Generator: i1Generator,
    DNSPresets: DNSPresets,
    JunkPresets: JunkPresets,
    EndpointPresets: EndpointPresets,
    randomEndpoint: randomEndpoint,
    LIMITS: LIMITS,
    WARP_SAFE: WARP_SAFE,
    API: API,
    CONFIG: CONFIG
  };
})(typeof window !== 'undefined' ? window : (typeof self !== 'undefined' ? self : this));