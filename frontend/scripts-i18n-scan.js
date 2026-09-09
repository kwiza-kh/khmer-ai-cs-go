const fs = require("fs"), path = require("path");
function walk(d, acc = []) {
  for (const f of fs.readdirSync(d)) {
    const p = path.join(d, f);
    const st = fs.statSync(p);
    if (st.isDirectory()) walk(p, acc);
    else if (/\.(tsx?|jsx?)$/.test(f)) acc.push(p);
  }
  return acc;
}
const files = walk("src");

function dictKeys(file) {
  const s = fs.readFileSync(file, "utf8");
  const out = new Set();
  const re = /^\s*"([^"]+)"\s*:/gm;
  let m;
  while ((m = re.exec(s))) out.add(m[1]);
  return out;
}
const en = dictKeys("src/lib/i18n/dict-en.ts");
const zh = dictKeys("src/lib/i18n/dict-zh.ts");
const km = dictKeys("src/lib/i18n/dict-km.ts");

function dictValue(file, key) {
  const s = fs.readFileSync(file, "utf8");
  const idx = s.indexOf('"' + key + '"');
  if (idx < 0) return null;
  const rest = s.slice(idx);
  const m = rest.match(/^\s*"[^"]+"\s*:\s*"((?:[^"\\]|\\.)*)"/);
  return m ? m[1] : null;
}

// ---- dynamic template keys ----
const dyn = [];
for (const f of files) {
  const src = fs.readFileSync(f, "utf8");
  const lines = src.split(/\r?\n/);
  lines.forEach((line, i) => {
    const re = /\bt(?:f)?\(\s*`([^`]+)`/g;
    let m;
    while ((m = re.exec(line))) dyn.push({ f, line: i + 1, tpl: m[1] });
  });
}
console.log("=== DYNAMIC t(`...`) KEYS (" + dyn.length + ") ===");
dyn.forEach((d) => console.log("  " + d.f + ":" + d.line + "  `" + d.tpl + "`"));

// Resolve known dynamic families
console.log("\n=== RESOLVE platforms pf.<label>.* ===");
const labels = ["messenger", "instagram", "telegram", "whatsapp", "line", "zalo"];
const suffixes = ["name", "desc", "inbound", "outbound", "receipts", "limitation", "webhookInfo"];
for (const l of labels) {
  for (const s of suffixes) {
    const k = `pf.${l}.${s}`;
    if (!en.has(k) || !zh.has(k) || !km.has(k))
      console.log("  MISSING", k, "en:" + en.has(k), "zh:" + zh.has(k), "km:" + km.has(k));
  }
}
// discover actual label keys used in platforms/page.tsx
const pfSrc = fs.readFileSync("src/app/platforms/page.tsx", "utf8");
const pfKeys = [...pfSrc.matchAll(/label:\s*"([^"]+)"/g)].map((x) => x[1]);
console.log("  labels found in source:", JSON.stringify([...new Set(pfKeys)]));

// ---- tf interpolation checks ----
console.log("\n=== tf() PLACEHOLDER MISMATCHES ===");
let tfIssues = 0;
for (const f of files) {
  const src = fs.readFileSync(f, "utf8");
  const re = /\btf\(\s*["']([A-Za-z0-9_.\-]+)["']\s*,\s*\{([\s\S]*?)\}\s*\)/g;
  let m;
  while ((m = re.exec(src))) {
    const line = src.slice(0, m.index).split("\n").length;
    const key = m[1];
    const params = [...m[2].matchAll(/([A-Za-z0-9_]+)\s*:/g)].map((x) => x[1]);
    for (const [lang, set, file] of [["en", en, "src/lib/i18n/dict-en.ts"], ["zh", zh, "src/lib/i18n/dict-zh.ts"], ["km", km, "src/lib/i18n/dict-km.ts"]]) {
      const v = dictValue(file, key);
      if (v == null) continue;
      const ph = [...v.matchAll(/\{([A-Za-z0-9_]+)\}/g)].map((x) => x[1]);
      const missing = ph.filter((p) => !params.includes(p));
      const unused = params.filter((n) => !ph.includes(n));
      if (missing.length || unused.length) {
        tfIssues++;
        console.log(`  ${f}:${line} [${lang}] ${key} params=${JSON.stringify(params)} ph=${JSON.stringify(ph)} MISSING=${JSON.stringify(missing)} UNUSED=${JSON.stringify(unused)}`);
      }
    }
  }
}
if (!tfIssues) console.log("  none");
