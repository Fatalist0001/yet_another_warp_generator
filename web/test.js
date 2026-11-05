// Тесты warpgen.js без браузера: crypto и fetch подменяются на Node.
// Запуск: node test.js
const crypto = require('crypto');
const fs = require('fs');

global.crypto = {
  getRandomValues: function (arr) { crypto.randomFillSync(Buffer.from(arr.buffer, arr.byteOffset, arr.byteLength)); return arr; },
  subtle: crypto.webcrypto.subtle
};

const W = new Function(fs.readFileSync('warpgen.js', 'utf8') + '; return global.Warpgen')();
console.log('Warpgen загружен, версия', W.version);

let failures = 0;
function check(name, cond, extra) {
  console.log('   ', cond ? '✓' : '✗', name + (extra ? ' — ' + extra : ''));
  if (!cond) failures++;
}

async function main() {
  // ---------- 1. ключи ----------
  console.log('\n[1] Генерация ключей WireGuard (3 раза)');
  for (let i = 0; i < 3; i++) {
    const kp = W.generateKeypair();
    check('ключи 32 байта', Buffer.from(kp.private, 'base64').length === 32 &&
      Buffer.from(kp.public, 'base64').length === 32);
    check('clamp применён', (Buffer.from(kp.private, 'base64')[0] & 7) === 0);
  }
  // Вектор RFC 7748 §5.2 (X25519: base 9)
  const rfc = new Function(fs.readFileSync('warpgen.js', 'utf8')
    .replace('  global.Warpgen = {', '  global.__x = { x25519Big: x25519Big },\n  global.Warpgen = {') + '; return global.__x;')();
  const scalar = Buffer.from('77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a', 'hex');
  const base = new Uint8Array(32); base[0] = 9;
  check('RFC 7748 вектор',
    Buffer.from(rfc.x25519Big(new Uint8Array(scalar), base)).toString('hex') ===
    '8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a');

  // ---------- 2. I1 ----------
  console.log('\n[2] I1-генераторы');
  for (const p of ['quic', 'dns', 'sip', 'stun']) {
    const res = await W.i1Generator.generate(p, 'www.google.com');
    check(p + ' → цепочка валидна', W.CONFIG.validateI1(res.chain), res.label);
  }
  check('random профиль', W.CONFIG.validateI1((await W.i1Generator.generate('random')).chain));
  check('статический I1 валиден', W.CONFIG.validateI1(W.i1Generator.defaults()));
  check('profiles() = 4', W.i1Generator.profiles().length === 4);

  // ---------- 3. валидация I1 ----------
  console.log('\n[3] Валидация I1-цепочки');
  check('хорошая цепочка', W.CONFIG.validateI1('<r 2><b 0x8580><r 0>'));
  check('пустая отклонена', !W.CONFIG.validateI1(''));
  check('другие директивы отклонены', !W.CONFIG.validateI1('<b 0x85><s 1>'));
  check('rp/rc/rd разрешены', W.CONFIG.validateI1('<b 0x85><rc 10><rd 4><r 0>'));

  // ---------- 4. пресеты ----------
  console.log('\n[4] Пресеты');
  check('DNS-пресетов 8', W.DNSPresets.length === 8, W.DNSPresets.map(p => p.id).join(','));
  check('рекомендуемый — первый и по умолчанию', W.DNSPresets[0].id === 'recommended' && W.DNSPresets[0].def);
  check('junk-пресетов 6', W.JunkPresets.length === 6);
  check('дефолт junk = warpgen (4/40/70)',
    (() => { const p = W.JunkPresets.find(x => x.def); return p.jc === 4 && p.jmin === 40 && p.jmax === 70; })());

  // ---------- 5. endpoint ----------
  console.log('\n[5] Случайный endpoint');
  for (let i = 0; i < 50; i++) {
    const ep = W.randomEndpoint({ widePorts: i % 2 === 0 });
    if (!/^(?:\d{1,3}\.){3}\d{1,3}:\d+$/.test(ep)) throw new Error('плохой endpoint: ' + ep);
    const port = Number(ep.split(':')[1]);
    if (!(port >= 1 && port <= 65535)) throw new Error('плохой порт: ' + ep);
  }
  check('50 endpoint\'ов в формате ip:port', true);
  const warpOnly = W.randomEndpoint({ port: 2408 });
  check('порт задан явно', warpOnly.endsWith(':2408'), warpOnly);

  // ---------- 6. сборка конфига ----------
  console.log('\n[6] Сборка .conf (аккаунт от сервера)');
  const account = {
    privateKey: 'aGVsbG8td29ybGQtcHJpdmF0ZS1rZXktMzItYnl0ZXMhIQ==',
    serverPublicKey: 'bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=',
    addressV4: '172.16.0.2',
    addressV6: '2606:4700:110:8f2e:f22e:f22e:8a2e:e12e'
  };
  const cfg = await W.CONFIG.build({
    account: account,
    endpoint: '162.159.192.1:2408',
    dns: '1.1.1.1, 8.8.8.8',
    junk: 'warpgen',
    i1Profile: 'dns',
    i1Host: 'dns.geohide.ru'
  });
  const res = await W.CONFIG.generate(cfg);
  const direct = await W.CONFIG.generate({ account, endpoint: '188.114.96.3:500', dns: '1.1.1.1' });
  check('generate(input) работает без явного build', direct.ok === true && !!direct.conf);
  check('конфиг собран', res.ok, res.errors.join('; '));
  const conf = res.conf;

  const order = ['[Interface]', 'PrivateKey', 'Address', 'DNS', 'MTU', 'S1', 'S2', 'S3', 'S4',
    'Jc', 'Jmin', 'Jmax', 'H1', 'H2', 'H3', 'H4', 'I1', '[Peer]', 'PublicKey', 'AllowedIPs',
    'Endpoint', 'PersistentKeepalive'];
  let pos = -1, orderOk = true;
  for (const key of order) {
    const next = conf.indexOf(key, pos + 1);
    if (next < 0 || next < pos) { orderOk = false; console.log('   нет/не по порядку:', key); }
    pos = next;
  }
  check('порядок полей как в config.Generate', orderOk);
  check('S1..S4 = 0', /S1 = 0\nS2 = 0\nS3 = 0\nS4 = 0/.test(conf));
  check('H1..H4 = 1,2,3,4', /H1 = 1\nH2 = 2\nH3 = 3\nH4 = 4/.test(conf));
  check('MTU = 1280', conf.includes('MTU = 1280'));
  check('Jc/Jmin/Jmax = 4/40/70', conf.includes('Jc = 4\nJmin = 40\nJmax = 70'));
  check('Address с IPv4 и IPv6', conf.includes('Address = 172.16.0.2, 2606:4700:110:8f2e:f22e:f22e:8a2e:e12e'));
  check('AllowedIPs = 0.0.0.0/0, ::/0', conf.includes('AllowedIPs = 0.0.0.0/0, ::/0'));
  var iface = conf.split('[Peer]')[0];
  var peer = conf.split('[Peer]')[1] || '';
  check('PersistentKeepalive только в [Peer]',
    peer.includes('PersistentKeepalive = 25') && !iface.includes('PersistentKeepalive'));
  check('I1 — заглавное поле', /^I1 = </m.test(conf) && !/^i1 = /m.test(conf));
  check('I1 профиль dns(dns.geohide.ru)', cfg.i1Label === 'dns(dns.geohide.ru)', cfg.i1Label);

  // ---------- 7. custom I1 и режим «без I1» ----------
  console.log('\n[7] custom I1 и отказ на пустом');
  const customChain = '<r 2><b 0x858000010001000000000669636c6f756403636f6d0000010001c00c000100010000105a00044d583737>';
  const cfg2 = await W.CONFIG.build({ account, endpoint: '188.114.96.3:500', dns: '1.1.1.1', i1: customChain });
  const res2 = await W.CONFIG.generate(cfg2);
  check('custom I1 принят', res2.ok && res2.conf.includes('I1 = ' + customChain), res2.errors.join('; '));

  const cfgNo = await W.CONFIG.build({ account, endpoint: '188.114.96.3:500', dns: '1.1.1.1', i1Profile: 'none' });
  const resNo = await W.CONFIG.generate(cfgNo);
  check('режим «без I1» — строки нет', resNo.ok && !resNo.conf.includes('I1 = '));

  const badI1 = await W.CONFIG.build({ account, endpoint: '188.114.96.3:500', dns: '1.1.1.1', i1: '<b 0x85><evil 1>' });
  const badI1res = await W.CONFIG.generate(badI1);
  check('битая цепочка отклонена', !badI1res.ok &&
    badI1res.errors.some(e => e.includes('I1')), badI1res.errors.join('; '));

  // ---------- 8. ошибки валидации ----------
  console.log('\n[8] Ошибки валидации конфига');
  const noAccount = await W.CONFIG.generate(await W.CONFIG.build({ endpoint: '1.2.3.4:500' }));
  check('без ключей — ошибка', !noAccount.ok && noAccount.errors.length >= 2, noAccount.errors.join('; '));

  const badMtu = await W.CONFIG.generate(await W.CONFIG.build({
    account, endpoint: '1.2.3.4:500', dns: '1.1.1.1', mtu: 1500
  }));
  check('MTU 1500 отклонён', !badMtu.ok, badMtu.errors.join('; '));

  const badEp = await W.CONFIG.generate(await W.CONFIG.build({
    account, endpoint: 'нет-порта', dns: '1.1.1.1'
  }));
  check('endpoint без порта отклонён', !badEp.ok, badEp.errors.join('; '));

  const badJunk = await W.CONFIG.generate(await W.CONFIG.build({
    account, endpoint: '1.2.3.4:500', dns: '1.1.1.1', junk: { jc: 4, jmin: 100, jmax: 50 }
  }));
  check('Jmax <= Jmin отклонён', !badJunk.ok, badJunk.errors.join('; '));

  global.fetch = async () => ({ ok: false, status: 502, text: async () => 'bad gateway' });
  const badDns2 = await W.CONFIG.build({ account, endpoint: '1.2.3.4:500', dns: 'dns.example.invalid' });
  check('нерезолвящийся DNS — ошибка', !await W.CONFIG.generate(badDns2).ok, badDns2.dnsErrors.join('; '));

  check('isIP: валидные IPv4/IPv6',
    W.CONFIG.isIP('1.1.1.1') && W.CONFIG.isIP('2606:4700:110::1') && W.CONFIG.isIP('::1'));
  check('isIP: 999.1.1.1 отклонён', !W.CONFIG.isIP('999.1.1.1'));
  check('isIP: битый IPv6 отклонён',
    !W.CONFIG.isIP(':::') && !W.CONFIG.isIP('1::2::3') && !W.CONFIG.isIP('1:2:3:4:5:6:7:8:9'));

  // ---------- 9. API-клиент ----------
  console.log('\n[9] API-клиент сервера');
  const jsonResp = (status, obj) => ({
    ok: status >= 200 && status < 300, status,
    text: async () => JSON.stringify(obj)
  });

  global.fetch = async () => jsonResp(200, {
    privKey: account.privateKey, peer_pub: account.serverPublicKey,
    client_ipv4: '172.16.0.2/32', client_ipv6: '2606:4700::1/128', endpoint: '162.159.192.1:2408'
  });
  const acc = await W.API.account();
  check('account(): контракт ref1', acc.privateKey === account.privateKey &&
    acc.serverPublicKey === account.serverPublicKey && acc.addressV4 === '172.16.0.2/32');

  global.fetch = async () => jsonResp(200, {
    success: true,
    registration: { privKey: 'p', ipv4: '172.16.0.3', ipv6: '2606:4700::2', peerPublicKey: 'peer', endpoint: 'e:1' }
  });
  const acc2 = await W.API.account();
  check('account(): обёртка registration тоже понимается', acc2.addressV4 === '172.16.0.3' &&
    acc2.serverPublicKey === 'peer' && acc2.privateKey === 'p');

  global.fetch = async () => jsonResp(200, { success: true, registration: { ipv4: '172.16.0.3', peerPublicKey: 'peer' } });
  let noPriv = '';
  try { await W.API.account(); } catch (e) { noPriv = e.message; }
  check('account(): ответ без приватного ключа отклонён', noPriv.includes('privKey'), noPriv);

  global.fetch = async () => jsonResp(200, { privKey: 'x' });
  let code = '';
  try { await W.API.account(); } catch (e) { code = e.code; }
  check('account(): неполный ответ → BAD_RESPONSE', code === 'BAD_RESPONSE', code);

  global.fetch = async () => jsonResp(429, { error: 'слишком много запросов', code: 'RATE_LIMITED' });
  let rl = null;
  try { await W.API.account(); } catch (e) { rl = e; }
  check('account(): 429 → RATE_LIMITED', rl && rl.code === 'RATE_LIMITED', rl && rl.message);

  global.fetch = async () => { throw new Error('connect ECONNREFUSED'); };
  let net = null;
  try { await W.API.account(); } catch (e) { net = e; }
  check('account(): нет сети → NETWORK_ERROR', net && net.code === 'NETWORK_ERROR');

  global.fetch = async () => jsonResp(200, {
    presets: [
      { id: 'comss', label: 'Comss', host: 'dns.comss.one', ips: ['1.2.3.4'], ok: true, rttMs: 21 },
      { id: 'malw', label: 'Malw', host: 'dns.malw.link', ips: [], ok: false, rttMs: null }
    ]
  });
  const dns = await W.API.dns();
  check('dns(): нормализация', dns.length === 2 && dns[0].ips[0] === '1.2.3.4' &&
    dns[0].ok === true && dns[1].ok === false);

  // ---------- 10. QUIC Initial: независимая расшифровка ----------
  console.log('\n[10] QUIC Initial: расшифровка нативным crypto');
  const quicSrc = fs.readFileSync('warpgen.js', 'utf8')
    .replace('  global.Warpgen = {',
      '  global.__quic = { quicInitialPacket: quicInitialPacket, quicInitialKeys: quicInitialKeys,' +
      ' tlsClientHello: tlsClientHello, quicCryptoFrame: quicCryptoFrame, quicInitialSalt: quicInitialSalt },\n  global.Warpgen = {');
  const WQ = new Function(quicSrc + '; return { W: global.Warpgen, q: global.__quic };')();
  const q = WQ.q;

  const hmac = (key, data) => crypto.createHmac('sha256', key).update(data).digest();
  const expand = (secret, label, len) => {
    const full = Buffer.from('tls13 ' + label);
    const info = Buffer.concat([Buffer.from([(len >> 8) & 0xff, len & 0xff]), Buffer.from([full.length]), full, Buffer.from([0, 1])]);
    return hmac(secret, info).slice(0, len);
  };
  const refKeys = (dcid) => {
    const initial = hmac(Buffer.from(q.quicInitialSalt), dcid);
    const client = expand(initial, 'client in', 32);
    return { key: expand(client, 'quic key', 16), iv: expand(client, 'quic iv', 12), hp: expand(client, 'quic hp', 16) };
  };

  const dcid = new Uint8Array([0x42]);
  const pkn = new Uint8Array([0]);
  const hello = q.tlsClientHello('www.google.com');
  const payload = q.quicCryptoFrame(hello);
  const built = await q.quicInitialPacket(dcid, pkn, payload);
  const packet = Buffer.from(built.packet);
  const hl = built.headerLen;

  const k = refKeys(Buffer.from(dcid));
  const ciphertext = packet.slice(hl);
  const sample = ciphertext.slice(4 - pkn.length, 20 - pkn.length);
  const ecb = crypto.createCipheriv('aes-128-ecb', k.hp, null);
  ecb.setAutoPadding(false);
  const mask = Buffer.concat([ecb.update(sample), ecb.final()]);

  const header = Buffer.from(packet.slice(0, hl));
  header[0] ^= mask[0] & 0x0f;
  for (let i = 0; i < pkn.length; i++) header[hl - pkn.length + i] ^= mask[1 + i];

  const iv = Buffer.from(k.iv);
  for (let i = 0; i < pkn.length; i++) iv[iv.length - pkn.length + i] ^= header[hl - pkn.length + i];

  const gcm = crypto.createDecipheriv('aes-128-gcm', k.key, iv);
  gcm.setAAD(header);
  gcm.setAuthTag(ciphertext.slice(ciphertext.length - 16));
  const plain = Buffer.concat([gcm.update(ciphertext.slice(0, ciphertext.length - 16)), gcm.final()]);

  check('версия QUIC v1 (0x00000001)', header.slice(1, 5).toString('hex') === '00000001');
  check('флаг 0xC0 + pnlen=0 (Initial)', (header[0] & 0xc0) === 0xc0 && (header[0] & 0x03) === 0x00);
  check('DCID совпадает (str8: len=1, 0x42)', header[5] === 1 && header[6] === 0x42);
  check('длины SCID/token = 0', header[7] === 0 && header[8] === 0);
  check('открытый текст = payload (AES-128-GCM, AAD = заголовок)', plain.equals(Buffer.from(payload)));
  check('длина varint заголовка 2 байта', (header[9] & 0xc0) === 0x40);

  const chain = (await WQ.W.i1Generator.generate('quic', 'www.google.com')).chain;
  const totalHex = (chain.match(/<b 0x([0-9a-f]+)>/g) || []).reduce((a, s) => a + (s.length - 6), 0) / 2;
  const totalRnd = (chain.match(/<(?:r|rc|rd) (\d+)>/g) || []).reduce((a, s) => a + parseInt(s.match(/(\d+)/)[1], 10), 0);
  check('длина CPS-цепочки = длине пакета', totalHex + totalRnd === packet.length,
    (totalHex + totalRnd) + ' vs ' + packet.length);

  if (failures) throw new Error('провалено проверок: ' + failures);
  console.log('\nALL TESTS PASSED');
}

main().catch(e => {
  console.error('TEST FAILED:', e.stack || e);
  process.exit(1);
});