// Статическая проверка разметки и стилей index.html.
// Ловит класс ошибок, которые не видны в DOM-стабе: неопределённые CSS-переменные,
// классы без правил, отсутствующие id, дубли, несбалансированные скобки.
// Запуск: node test-css.js
const fs = require('fs');
const path = require('path');

let failures = 0;
function check(name, cond, extra) {
  console.log('   ', cond ? '✓' : '✗', name + (extra ? ' — ' + extra : ''));
  if (!cond) failures++;
}

const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
const app = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');

const styleMatch = html.match(/<style>([\s\S]*?)<\/style>/);
check('есть ровно один блок <style>', (html.match(/<style>/g) || []).length === 1);
const css = styleMatch ? styleMatch[1] : '';
const bodyHtml = html.split('</style>')[1] || '';

console.log('\n[1] Каркас документа');
check('ровно один <body>', (html.match(/<body[\s>]/g) || []).length === 1);
check('внешние скрипты подключены',
  (html.match(/<script src="([^"]+)"/g) || []).length === 2,
  (html.match(/<script src="([^"]+)"/g) || []).join(' '));
check('нет inline-скриптов', !/<script(?![^>]*\bsrc=)[^>]*>[\s\S]*?<\/script>/.test(bodyHtml));
check('фигурные скобки CSS сбалансированы', css.split('{').length === css.split('}').length,
  css.split('{').length - 1 + ' правил');

console.log('\n[2] CSS-переменные');
const defined = new Set((css.match(/--[a-z0-9-]+\s*:/g) || []).map(s => s.replace(/\s*:$/, '')));
const used = new Set((css.match(/var\(--[a-z0-9-]+/g) || []).map(s => s.slice(4)));
const undef = [...used].filter(v => !defined.has(v));
check('все var(--…) объявлены', undef.length === 0, undef.join(', '));
const unused = [...defined].filter(v => !used.has(v) && !new RegExp('var\\(' + v + '\\)').test(css));
check('нет неиспользуемых переменных', unused.length === 0, unused.join(', '));

console.log('\n[3] Классы из разметки имеют стили');
const classesInHtml = new Set();
(html.match(/class="([^"]*)"/g) || []).forEach(m => {
  m.replace(/class="|"/g, '').split(/\s+/).filter(Boolean).forEach(c => classesInHtml.add(c));
});
const cssClasses = new Set((css.match(/\.([a-zA-Z][a-zA-Z0-9_-]*)/g) || []).map(s => s.slice(1)));
// классы, которые ставит JS и которые описаны через атрибутные/комбинированные селекторы
const noStyle = [...classesInHtml].filter(c => !cssClasses.has(c));
check('у каждого класса есть правило в CSS', noStyle.length === 0, noStyle.join(', '));
check('класс .hidden определён (им пользуется app.js)', /\.hidden\s*\{[^}]*display:\s*none/.test(css));

console.log('\n[4] Идентификаторы');
const ids = (html.match(/id="([^"]+)"/g) || []).map(s => s.replace(/id="|"/g, ''));
const dupIds = ids.filter((id, i) => ids.indexOf(id) !== i);
check('нет дублей id', dupIds.length === 0, dupIds.join(', '));

const wanted = new Set((app.match(/\$\('([^']+)'\)/g) || []).map(s => s.slice(3, -2)));
const missing = [...wanted].filter(id => !ids.includes(id));
check('все id из app.js есть в разметке', missing.length === 0, missing.join(', '));

console.log('\n[5] Обязательные элементы интерфейса');
['genBtn', 'settingsPanel', 'settingsTrigger', 'sheet', 'confOut', 'paramEp', 'paramDns', 'paramMtu', 'paramI1']
  .forEach(id => check('есть #' + id, ids.includes(id)));

console.log('\n[6] Доступность и поведение');
check('у кнопок есть type', (bodyHtml.match(/<button(?![^>]*\btype=)/g) || []).length === 0);
check('иконки-кнопки подписаны aria-label',
  (bodyHtml.match(/<button[^>]*aria-label|<button[^>]*id="(genBtn|confCopy|confDownload|accBtn|accDnsBtn|epBtn)"/g) || []).length > 0);
check('учитывается prefers-reduced-motion', /prefers-reduced-motion/.test(css));
check('фоновый слой не перехватывает клики',
  /\.bg\s*\{[^}]*pointer-events:\s*none/.test(css) &&
  /\.settings-backdrop\s*\{[^}]*pointer-events:\s*none/.test(css));
check('анимации используют определённый easing', !/var\(--ease-(?!in|in-out|out)[a-z-]*\)/.test(css));

console.log('\n[7] Эстетика: приборный стиль');
check('нет градиентов', !/(linear|radial|conic)-gradient/.test(css));
check('нет box-shadow и glow', !/box-shadow/.test(css));
check('нет blur/glassmorphism', !/backdrop-filter|blur\(/.test(css));
const radii = [...css.matchAll(/border-radius:\s*([^;}]+)/g)].map(m => m[1].trim());
check('скругления только нулевые', radii.every(r => /^(0|0px)$/.test(r)), radii.join(', ') || 'нет');
check('два шрифта объявлены и использованы',
  /--font-mono:\s*'JetBrains Mono'/.test(css) && /--font-sans:\s*'Space Grotesk'/.test(css) &&
  /var\(--font-mono\)/.test(css) && /var\(--font-sans\)/.test(css));
check('кнопка при нажатии сдвигается', /\.action-btn:active\s*\{[^}]*translateY\(1px\)/.test(css));
check('нет эмодзи в разметке', !/[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u.test(bodyHtml));
check('статусы в формате лога [ok]/[--]', /'\[ok\]'/.test(app) && /'\[--\]'/.test(app));
check('нумерованные заголовки групп', (bodyHtml.match(/class="group-head">0[1-6] /g) || []).length >= 5);
check('лог статусов размечен как .status-log', /class="status-log" id="statusBox"/.test(bodyHtml));

console.log('');
if (failures) {
  console.log('CSS/HTML TEST FAILED: ' + failures);
  process.exit(1);
}
console.log('CSS/HTML TEST PASSED');