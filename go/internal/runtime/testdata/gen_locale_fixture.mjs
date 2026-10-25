#!/usr/bin/env node
// Generates the collation fixtures the Go port is differential-tested against.
//
//   node go/internal/runtime/testdata/gen_locale_fixture.mjs
//
// The reference is Node's default `Intl.Collator` / String.prototype.localeCompare
// (ICU with the process default locale), which is exactly what the TypeScript
// implementation calls at the four sort sites this task covers. Two fixtures are
// written next to this script:
//
//   locale_env.json     how Node/ICU derives the collation locale from
//                       LANG/LC_ALL/LC_COLLATE/LC_MESSAGES, plus the collation
//                       order of a fixed probe list under each environment.
//   collate_corpus.json a >=20k-name corpus with Node's stable sorted order and
//                       sampled pairwise comparison signs for en-US, C,
//                       es-ES, sv-SE and tr-TR.
//
// Everything is deterministic: no Date, no Math.random, no locale-dependent
// input beyond the env scenarios being measured.
import { spawnSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const nodeVersion = process.version;
const { icu, cldr, unicode } = process.versions;

// --- fixed probe list -------------------------------------------------------
// One entry per collation-relevant class: accented Latin, plain Latin, case
// variants, Turkish dotless/dotted i, sharp s, ligatures, CJK, emoji, digits,
// punctuation, spacing, NFC vs NFD and combining marks.
const probe = [
  "é", "e", "E", "z", "ñ", "n", "å", "ä", "ö", "ı", "İ", "i", "ß", "ss",
  "Æ", "œ", "中", "日", "😀", "0", "9", " ", "-", "_", ".", "A", "a", "Z",
  "e\u0301", "x", "X", "ÿ", "y", "oe", "ab", "a b", "a-b", "a_b", "1", "01",
  "10", "2", "a1", "a01", "İstanbul", "ıstanbul", "istanbul", "ßa", "ssb",
  "Straße", "Strasse", "\u00adsoft", "ｆｕｌｌ", "ﾊﾝｶｸ", "①", "Ⅻ", "ⅰ",
];

const probeIndex = new Map(probe.map((value, index) => [value, index]));
if (probeIndex.size !== probe.length) throw new Error("probe list has duplicates");

function sortedProbe(locale) {
  return [...probe]
    .sort((a, b) => (locale === undefined ? a.localeCompare(b) : a.localeCompare(b, locale)))
    .map((value) => probeIndex.get(value));
}

// Runs a child Node with exactly `env` (nothing inherited) and reports the
// locale its default collator resolves to plus the probe order.
function measureEnv(env) {
  const script = `
    const probe = ${JSON.stringify(probe)};
    const order = [...probe].sort((a, b) => a.localeCompare(b));
    process.stdout.write(JSON.stringify({
      resolved: new Intl.Collator().resolvedOptions().locale,
      order,
    }));
  `;
  const result = spawnSync(process.execPath, ["-e", script], {
    env,
    encoding: "utf8",
    maxBuffer: 1 << 20,
  });
  if (result.status !== 0) throw new Error(result.stderr || `child exited ${result.status}`);
  return JSON.parse(result.stdout);
}

const values = [
  "C",
  "POSIX",
  "C.UTF-8",
  "en_US.UTF-8",
  "es_ES.UTF-8",
  "de_DE.UTF-8",
  "sv_SE.UTF-8",
  "tr_TR.UTF-8",
  "ja_JP.UTF-8",
  "invalid",
];

// Scenario name -> env passed to the child. `unset` scenarios pass an empty
// environment; empty-string scenarios pass the variable with an empty value,
// which ICU treats differently from an absent variable.
const scenarios = [{ name: "unset", env: {} }];
for (const value of values) {
  scenarios.push({ name: `LANG=${value}`, env: { LANG: value } });
  scenarios.push({ name: `LC_ALL=${value}`, env: { LC_ALL: value } });
  scenarios.push({ name: `LC_COLLATE=${value}`, env: { LC_COLLATE: value } });
  scenarios.push({ name: `LANG=LC_ALL=${value}`, env: { LANG: value, LC_ALL: value } });
}
scenarios.push(
  { name: "LC_ALL=''", env: { LC_ALL: "" } },
  { name: "LC_ALL='' LANG=C", env: { LC_ALL: "", LANG: "C" } },
  { name: "LC_ALL='' LANG=en_US.UTF-8", env: { LC_ALL: "", LANG: "en_US.UTF-8" } },
  { name: "LC_ALL='' LANG=sv_SE.UTF-8", env: { LC_ALL: "", LANG: "sv_SE.UTF-8" } },
  { name: "LC_MESSAGES=sv_SE.UTF-8", env: { LC_MESSAGES: "sv_SE.UTF-8" } },
  { name: "LANG=en_US.UTF-8 LC_MESSAGES=sv_SE.UTF-8", env: { LANG: "en_US.UTF-8", LC_MESSAGES: "sv_SE.UTF-8" } },
  { name: "LANG=tr_TR.UTF-8@euro", env: { LANG: "tr_TR.UTF-8@euro" } },
  { name: "LANG=C.utf8", env: { LANG: "C.utf8" } },
  { name: "LANG=bogus_XX.UTF-8", env: { LANG: "bogus_XX.UTF-8" } },
);

const envFixture = {
  generator: "go/internal/runtime/testdata/gen_locale_fixture.mjs",
  node: nodeVersion,
  icu,
  cldr,
  unicode,
  probe,
  scenarios: scenarios.map(({ name, env }) => {
    const { resolved, order } = measureEnv(env);
    return { name, env, resolved, order: order.map((value) => probeIndex.get(value)) };
  }),
};

writeFileSync(join(here, "locale_env.json"), JSON.stringify(envFixture, null, 1) + "\n");

// --- differential corpus ----------------------------------------------------
// Deterministic 32-bit PRNG (mulberry32) so the corpus is reproducible without
// depending on Math.random.
function rng(seed) {
  let state = seed >>> 0;
  return () => {
    state = (state + 0x6d2b79f5) >>> 0;
    let t = state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const random = rng(0x5eed_1234);
const pick = (list) => list[Math.floor(random() * list.length)];
const chance = (p) => random() < p;

const latin = "abcdefghijklmnopqrstuvwxyz".split("");
const latinExtended = "àáâãäåæçèéêëìíîïðñòóôõöøùúûüýþÿāăąćčďđēĕėęěğġģħıĳįķĺļľłńņňōŏőœŕŗřśŝşšťţťŧũūŭůűųŵŷźżž".split("");
const greek = "αβγδεζηθικλμνξοπρστυφχψωάέήίόύώϊϋΐΰς".split("");
const cyrillic = "абвгдежзийклмнопрстуфхцчшщъыьэюяёђјљњћџ".split("");
const han = "中文汉字日本語测试東京都大阪山川田中人".split("");
const kana = "あいうえおかきくけこさしすせそたちつてとなにぬねのアイウエオカキクケコｱｲｳｴｵ".split("");
const hangul = "가나다라마바사아자차카타파하한국어서울".split("");
const arabic = "ابتثجحخدذرزسشصضطظعغفقكلمنهوي".split("");
const hebrew = "אבגדהוזחטיכלמנסעפצקרשת".split("");
const thai = "กขคงจฉชซญฎฏฐฑฒณดตถทธนบปผฝพฟภมยรลวศษสหฬอฮ".split("");
const devanagari = "अआइईउऊएऐओऔकखगघङचछजझञ".split("");
const emoji = ["😀", "🎉", "👍🏽", "👨‍👩‍👧‍👦", "🇪🇸", "☺️", "🀄", "🧑🏿‍🚀"];
const digits = "0123456789".split("");
const punctuation = [" ", "-", "_", ".", ",", ";", ":", "!", "?", "'", '"', "(", ")", "[", "]", "{", "}", "@", "*", "/", "\\", "&", "#", "%", "`", "^", "+", "<", "=", ">", "|", "~", "$"];
const widthForms = ["Ａ", "Ｂ", "Ｃ", "ａ", "ｂ", "①", "②", "Ⅻ", "ⅰ", "ｱ", "ｲ", "ｳ"];
const spaces = ["\u00a0", "\u2009", "\u3000", "\t"];

const corpus = new Set();
function add(name) {
  if (name.length === 0 || name.length > 80) return;
  corpus.add(name);
}

// Every probe entry is part of the corpus, plus suffixes so the ordering of
// equal-prefix names is exercised too.
for (const value of probe) {
  add(value);
  add(value + "1");
  add(value + "Z");
}

// 1. plain ASCII words, case variants and dotfiles.
const words = Array.from({ length: 600 }, () => {
  const length = 3 + Math.floor(random() * 8);
  let out = "";
  for (let i = 0; i < length; i += 1) out += pick(latin);
  return out;
});
for (const word of words) {
  add(word);
  add(word.toUpperCase());
  add(word[0].toUpperCase() + word.slice(1));
  add("." + word);
  add("." + word + ".txt");
  add(word + ".txt");
  add(word + ".TXT");
  add(word + "-" + pick(digits));
  add(word + "_" + pick(digits));
  add(word + "." + pick(digits));
}

// 2. accented and Latin Extended names, including NFC/NFD pairs.
for (const letter of latinExtended) {
  const base = pick(words);
  add(base + letter);
  add(letter + base);
  add(base.slice(0, 3) + letter + base.slice(3));
  const decomposed = letter.normalize("NFD");
  if (decomposed !== letter) {
    add(base + decomposed);
    add(decomposed + base);
    add(base.slice(0, 3) + decomposed + base.slice(3));
  }
  add(base + letter.toUpperCase());
}
// Explicit NFC vs NFD twins directly from combining sequences.
for (const pair of [
  ["é", "e\u0301"], ["ñ", "n\u0303"], ["ü", "u\u0308"], ["ç", "c\u0327"],
  ["å", "a\u030a"], ["ő", "o\u030b"], ["ğ", "g\u0306"], ["ś", "s\u0301"],
  ["ǹ", "n\u0300"], ["ḿ", "m\u0301"], ["ǘ", "u\u0308\u0301"], ["ǻ", "a\u030a\u0301"],
]) {
  add(pair[0] + "x");
  add(pair[1] + "x");
  add("x" + pair[0]);
  add("x" + pair[1]);
}

// 3. Greek, Cyrillic, Arabic, Hebrew, Thai, Devanagari.
for (const script of [greek, cyrillic, arabic, hebrew, thai, devanagari]) {
  for (let i = 0; i < 380; i += 1) {
    const length = 2 + Math.floor(random() * 6);
    let out = "";
    for (let j = 0; j < length; j += 1) out += pick(script);
    add(out);
    add(out + pick(digits));
    if (chance(0.3)) add("." + out);
    if (chance(0.3)) add(out + ".txt");
  }
}

// 4. CJK, kana, Hangul, fullwidth/halfwidth forms.
for (const script of [han, kana, hangul]) {
  for (let i = 0; i < 500; i += 1) {
    const length = 1 + Math.floor(random() * 5);
    let out = "";
    for (let j = 0; j < length; j += 1) out += pick(script);
    add(out);
    add(out + pick(digits));
    add(out + ".txt");
    add(out + pick(kana));
  }
}
for (const form of widthForms) {
  add(form);
  add("file" + form);
  add(form + "file");
  add(form + form + form);
}

// 5. Emoji, ZWJ sequences, variation selectors, skin tones, flags.
for (const glyph of emoji) {
  add(glyph);
  add(glyph + "x");
  add("x" + glyph);
  add(glyph + pick(digits));
  add("." + glyph);
}

// 6. Digits and numeric-ish names: lexical vs numeric order probe.
for (let i = 0; i < 400; i += 1) {
  const n = Math.floor(random() * 2000);
  add(String(n));
  add("0" + String(n));
  add("00" + String(n));
  add("file" + String(n));
  add("file" + String(n) + ".txt");
  add("v" + String(n % 30) + "." + String(n % 13));
  add(String(n % 30) + "-" + String(n % 13));
  add(String(n) + "a");
  add("a" + String(n));
  add("a0" + String(n));
  add("a00" + String(n));
}

// 7. Punctuation and space mixes, including names that differ only there.
for (let i = 0; i < 600; i += 1) {
  const left = pick(words);
  const right = pick(words);
  add(left + pick(punctuation) + right);
  add(left + pick(spaces) + right);
  add(pick(punctuation) + left);
  add(left + pick(punctuation) + pick(punctuation) + right);
  add(left + " " + pick(digits) + " " + right);
}

// 8. Case-only and accent-only twins, soft hyphens and other ignorables.
for (let i = 0; i < 500; i += 1) {
  const word = pick(words);
  add(word);
  add(word.toUpperCase());
  add(word.replace(/^./, (c) => c.toUpperCase()));
  const accented = pick(latinExtended);
  add(word + accented);
  add(word + accented.toUpperCase());
  add(word + "\u00ad");
  add("\u00ad" + word);
  add(word + "\u200b");
  add(word + "\u200d");
}

// Fill up to the target and guarantee the promised size.
const filler = ["a", "b", "z", "é", "ß", "中", "あ", "가", "ا", "א", "ก", "अ"];
let counter = 0;
while (corpus.size < 20500) {
  const base = pick(words) + pick(filler) + pick(punctuation) + String(counter);
  add(base);
  counter += 1;
}

const names = [...corpus];
if (names.length < 20000) throw new Error(`corpus too small: ${names.length}`);

// --- orders and pairwise signs ---------------------------------------------
const locales = [
  { key: "en_US.UTF-8", tag: undefined },
  { key: "C", tag: undefined },
  { key: "es_ES.UTF-8", tag: "es-ES" },
  { key: "sv_SE.UTF-8", tag: "sv-SE" },
  { key: "tr_TR.UTF-8", tag: "tr-TR" },
];

const index = new Map(names.map((name, i) => [name, i]));
const collatorFor = (locale) => new Intl.Collator(locale);
const compareWith = (locale) => {
  const collator = collatorFor(locale === "C" ? "en-US" : locale);
  return (a, b) => collator.compare(a, b);
};

const orders = {};
const pairs = {};
for (const { key, tag } of locales) {
  const compare = compareWith(tag);
  const sorted = [...names].sort(compare);
  orders[key] = sorted.map((name) => index.get(name));

  // Pairwise signs: 1200 pseudo-random pairs plus 600 adjacent pairs from the
  // sorted order (the hardest cases: accent/case/width neighbours).
  const sampled = [];
  const pairRandom = rng(0xc0ffee ^ key.length ^ names.length);
  const pairIndex = (limit) => Math.floor(pairRandom() * limit);
  for (let i = 0; i < 1200; i += 1) {
    const a = pairIndex(names.length);
    const b = pairIndex(names.length);
    sampled.push([a, b]);
  }
  for (let i = 0; i < 600; i += 1) {
    const a = pairIndex(names.length - 1);
    sampled.push([orders[key][a], orders[key][a + 1]]);
  }
  pairs[key] = sampled.map(([a, b]) => [a, b, Math.sign(compare(names[a], names[b])) || 0]);
}

const corpusFixture = {
  generator: "go/internal/runtime/testdata/gen_locale_fixture.mjs",
  node: nodeVersion,
  icu,
  cldr,
  unicode,
  locales: locales.map(({ key, tag }) => ({ envValue: key, locale: tag ?? "default" })),
  names,
  orders,
  pairs,
};

writeFileSync(join(here, "collate_corpus.json"), JSON.stringify(corpusFixture) + "\n");

// --- realistic filename corpus ----------------------------------------------
// The adversarial corpus above manufactures CLDR-skew neighbours on purpose.
// This one is what a user's project directory actually contains: accented
// Latin, German umlauts and sharp s, Spanish ñ, Turkish dotted/dotless i,
// Swedish å/ä/ö, Greek, Cyrillic, a handful of CJK/emoji names, NFC/NFD twins,
// numeric-looking names, dotfiles and mixed case. It must sort in exactly the
// same absolute positions as Node for every measured locale.
const realisticCategories = {
  "accented Latin": [
    "café.txt", "café.md", "naïve.txt", "résumé.pdf", "déjà-vu.txt", "façade.html",
    "cliché.txt", "crème-brûlée.txt", "jalapeño.txt", "piñata.txt", "über.txt",
    "übermäßig.txt", "müller.txt", "Müller.txt", "schön.txt", "schönheit.txt",
    "für.txt", "fünf.txt", "zürich.txt", "Zürich.txt", "élan.txt", "ÉLAN.txt",
    "garçon.txt", "leçon.txt", "père.txt", "mère.txt", "frère.txt", "sœur.txt",
    "noël.txt", "Noël.txt",
  ],
  "sharp s and umlauts": [
    "straße.txt", "STRASSE.txt", "strasse.txt", "weiß.txt", "weiss.txt",
    "groß.txt", "gross.txt", "fußball.txt", "FUSSBALL.txt", "größe.txt",
    "GRÖSSE.txt",
  ],
  "Spanish ñ": [
    "mañana.txt", "niño.txt", "niña.txt", "año.txt", "años.txt", "señor.txt",
    "señora.txt", "español.txt", "Español.txt", "muñeco.txt",
  ],
  "Swedish/Finnish åäö": [
    "ångström.txt", "Ångström.txt", "äpple.txt", "ärende.txt", "öppna.txt",
    "öl.txt", "kärlek.txt", "vänner.txt", "åsa.txt", "Åsa.txt", "smörgås.txt",
    "blåbär.txt",
  ],
  Turkish: [
    "istanbul.txt", "İstanbul.txt", "ıstanbul.txt", "İZMİR.txt", "izmir.txt",
    "ığdır.txt", "Iğdır.txt", "şehir.txt", "Şehir.txt", "çiçek.txt", "güneş.txt",
    "GÜNEŞ.txt",
  ],
  Greek: [
    "αθήνα.txt", "ΑΘΗΝΑ.txt", "αλφα.txt", "βητα.txt", "γάμμα.txt", "δέλτα.txt",
    "έψιλον.txt", "ζήτα.txt", "ήτα.txt", "θήτα.txt", "ελλάδα.txt", "ελληνικά.txt",
    "σίγμα.txt", "ωμέγα.txt", "π.txt", "φ.txt", "λ.txt", "μ.txt", "ν.txt", "ξ.txt",
  ],
  Cyrillic: [
    "москва.txt", "Москва.txt", "привет.txt", "Привет.txt", "ёлка.txt", "Ёлка.txt",
    "язык.txt", "Язык.txt", "україна.txt", "Київ.txt", "русский.txt", "РУССКИЙ.txt",
    "документ.txt", "Документ.txt", "отчёт.txt", "Отчёт.txt", "файл.txt", "Файл.txt",
    "тест.txt", "Тест.txt",
  ],
  "CJK and emoji": [
    "中文.txt", "日本語.md", "한국어.txt", "报告.txt", "テスト.txt", "😀.txt", "🎉.txt",
    "café-😀.txt", "x中文.txt", "中文-2.txt", "日本語.txt", "한국어.md",
  ],
  "NFC/NFD twins": [
    "café-receipt.txt", "cafe\u0301-receipt.txt", "A\u030angström.txt",
    "nandú.txt", "nandu\u0301.txt", "ḿ.txt", "m\u0301.txt", "e\u0301clair.txt",
  ],
  "digits and versions": [
    "file1.txt", "file2.txt", "file3.txt", "file10.txt", "file20.txt",
    "file100.txt", "v1.2.txt", "v1.10.txt", "2024-01-01.txt", "2024-12-31.txt",
    "a1.txt", "a01.txt", "01.txt", "10.txt", "2.txt", "1.txt", "0.txt",
    "007.txt", "3.14.txt", "1e6.txt",
  ],
  dotfiles: [
    ".gitignore", ".env", ".env.local", ".bashrc", ".zshrc", ".hidden", ".config",
    ".DS_Store", ".eslintrc.json", ".prettierrc", ".npmrc", ".editorconfig",
  ],
  "mixed case": [
    "README.md", "readme.md", "Readme.md", "Makefile", "makefile", "MAKEFILE",
    "Apple.txt", "apple.txt", "APPLE.txt", "Zebra.txt", "zebra.txt", "ZEBRA.txt",
    "Index.ts", "index.ts", "INDEX.TS", "Main.go", "main.go", "MAIN.GO",
  ],
  separators: [
    "my file.txt", "my-file.txt", "my_file.txt", "my.file.txt", "(draft).txt",
    "[final].txt", "note#1.txt", "100%.txt", "a+b.txt", "x=y.txt", "v2 (copy).txt",
    "todo-list.md", "TODO.md", "notes-2024.txt",
  ],
  "typical project files": [
    "index.js", "main_test.go", "package.json", "pnpm-lock.yaml", "tsconfig.json",
    "README", "LICENSE", "CHANGELOG.md", "Dockerfile", "docker-compose.yml",
    "config.yaml", "config.yml", "settings.json", "notes.txt", "todo.md",
    "photo.jpg", "image.PNG", "archive.tar.gz", "backup.zip", "script.sh",
    "build.log", "output.csv", "data.xml", "style.css", "app.tsx", "server.py",
    "requirements.txt", "go.mod",
  ],
};

const realisticNames = Object.values(realisticCategories).flat();
if (new Set(realisticNames).size !== realisticNames.length) {
  throw new Error("realistic corpus has duplicate names");
}
if (realisticNames.length < 200 || realisticNames.length > 500) {
  throw new Error(`realistic corpus out of range: ${realisticNames.length}`);
}

const realisticIndex = new Map(realisticNames.map((name, i) => [name, i]));
const realisticOrders = {};
for (const { key, tag } of locales) {
  const compare = compareWith(tag);
  realisticOrders[key] = [...realisticNames]
    .sort(compare)
    .map((name) => realisticIndex.get(name));
}

writeFileSync(
  join(here, "realistic_corpus.json"),
  JSON.stringify({
    generator: "go/internal/runtime/testdata/gen_locale_fixture.mjs",
    node: nodeVersion,
    icu,
    cldr,
    unicode,
    locales: locales.map(({ key, tag }) => ({ envValue: key, locale: tag ?? "default" })),
    categories: Object.fromEntries(
      Object.entries(realisticCategories).map(([name, values]) => [name, values.length]),
    ),
    names: realisticNames,
    orders: realisticOrders,
  }) + "\n",
);

process.stdout.write(
  `wrote locale_env.json (${envFixture.scenarios.length} scenarios), ` +
    `collate_corpus.json (${names.length} names) and ` +
    `realistic_corpus.json (${realisticNames.length} names)\n`,
);
