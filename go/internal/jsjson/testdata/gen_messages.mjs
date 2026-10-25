// Generates v8_errors.json from the Node in PATH.
//
// Run from this directory: node gen_messages.mjs
// The fixture records the exact JSON.parse SyntaxError message text; the Go
// test compares jsjson.SyntaxErrorMessage against it.
import { writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";

const curated = [
  // Empty / whitespace-only / BOM.
  "",
  " ",
  "\n\t\r ",
  "\uFEFF{}",
  " \uFEFF{}",

  // Literals.
  "not json",
  "n",
  "nu",
  "nul",
  "nulx",
  "nullx",
  "null x",
  "n ",
  "t",
  "f",
  "tru",
  "truE",
  "truex",
  "fals",
  "falsE",
  "falsex",
  "nulL",

  // Objects and arrays.
  "{",
  "[",
  "]",
  "}",
  "{,}",
  "{a:1}",
  "{'a':1}",
  "{\"a\" 1}",
  "{\"a\":}",
  "{\"a\":1,}",
  "{\"a\":1,,}",
  "{\"a\":1 \"b\":2}",
  "[,]",
  "[1,]",
  "[1 2]",
  "[\"a\" \"b\"]",
  "[[],]",
  "[[1],]",
  "{\"a\":{\"b\":[1,2,{\"c\":}}]}}",

  // Numbers.
  "01",
  "-01",
  "00",
  "-",
  "-x",
  "-Infinity",
  "1.",
  "1.e5",
  "1e",
  "1e+",
  "1.2e-",
  "1e\n",
  "+1",
  ".5",
  "1.2.3",
  "0x10",
  "-0x1",
  "1+2",

  // Special strings.
  "NaN",
  "Infinity",
  "undefined",
  "[object Object]",
  "NaN ",
  "Infinit",
  "undefine",

  // Trailing data.
  "1 2",
  "{}x",
  "[]x",
  "{\"a\":1}x",
  "{\"a\":1}extra",
  " {\"a\":1} x",
  "1 ",
  "1 \n",
  " {} ",
  "[1,2,3]",
  "[[[[[[[[[[1]]]]]]]]]]x",

  // Strings and escapes.
  "\"abc",
  "\"a\nb\"",
  "\"a\tb\"",
  "\"a\u0001b\"",
  "\"\\q\"",
  "\"\\x41\"",
  "\"\\u12\"",
  "\"\\u12g4\"",
  "\"\\u123g\"",
  "\"\\uD800\"",
  "\"\\\"",
  "\"\\",
  "\"\\u",
  "\"\\u1",
  "\"\\u123",
  "\"\\u1234",
  "\"\\é\"",
  "\"\\😀\"",
  "\"\u0000\"",
  "'x'",

  // Multibyte and UTF-16 positions.
  "[\"é\",1 2]",
  "\"😀\"x",
  "[\"😀\",x]",
  "[\"😀\",1 2]",
  "😀abc",
  "[\"😀😀😀😀😀😀😀😀\",x]",
  "[0,\"😀😀😀😀😀😀😀😀\",x]",
  "\u2028",
  " \u00a0",

  // Multi-line line/column.
  "{\n  \"a\": 1,\n  \"b\": 2,\n}",
  "[\n1,\n2\n,]",
  "[\n  1,\n  2\n  3\n]",
  "\r\n\r\n{",
  "\r{",
  "[\r\n1,\r\n]",
  "\n\n\n[1,2 3]",

  // Control characters.
  "\u0000",
  " \u0000",

  // Context window boundaries (kMaxContextCharacters = 10,
  // kMinOriginalSourceLengthForContext = 21).
  "x".repeat(20),
  "x".repeat(21),
  "x".repeat(30),
  "a".repeat(100),
  " ".repeat(10) + "x" + "a".repeat(10),
  " ".repeat(10) + "x" + "a".repeat(11),
  " ".repeat(11) + "x" + "a".repeat(9),
  "{" + "a".repeat(100) + "\": 1, bad}",
  "[1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,x]",
  "[" + "a".repeat(20) + ",x," + "b".repeat(30) + "]",
];

// Deterministic pseudo-random mutations widen coverage (positions, nested
// structures, context windows, multibyte text). mulberry32 keeps this
// reproducible across Node versions.
function mulberry32(seed) {
  return function () {
    seed |= 0;
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const alphabet = [
  ..."{}[],:\"\\ 0123456789.eE+-tfnNux",
  ..."é", "😀", "\n", "\t", "\r", "\u0001", "\uFEFF", "\u2028",
];
const docs = [
  '{"a":1,"b":[true,false,null],"c":"x"}',
  '[1,2,3,{"a":[4,5]},6]',
  '{"nested":{"deep":{"deeper":[1,2,{"k":"v"}]}}}',
  '["😀","é",123,"\\u0041","\\n"]',
  '{"a":"' + "x".repeat(60) + '","b":1}',
];

function mutate(rng, doc) {
  const chars = [...doc];
  const edits = 1 + Math.floor(rng() * 3);
  for (let i = 0; i < edits; i++) {
    const kind = Math.floor(rng() * 3);
    const at = Math.floor(rng() * (chars.length + 1));
    if (kind === 0) {
      chars.splice(at, 0, alphabet[Math.floor(rng() * alphabet.length)]);
    } else if (kind === 1 && chars.length > 0) {
      chars.splice(Math.min(at, chars.length - 1), 1);
    } else if (chars.length > 0) {
      chars[Math.min(at, chars.length - 1)] =
        alphabet[Math.floor(rng() * alphabet.length)];
    }
  }
  return chars.join("");
}

const random = [];
{
  const rng = mulberry32(0x5eed);
  for (let i = 0; i < 120; i++) {
    random.push(mutate(rng, docs[i % docs.length]));
  }
}

const inputs = [...curated, ...random];
const cases = inputs.map((input) => {
  let message = "";
  try {
    JSON.parse(input);
  } catch (error) {
    message = error.message;
  }
  return { input, message };
});

const nodeVersion = execFileSync("node", ["--version"], { encoding: "utf8" }).trim();
writeFileSync(
  new URL("./v8_errors.json", import.meta.url),
  JSON.stringify({ nodeVersion, template: "JSON.parse SyntaxError.message", cases }, null, 1) + "\n",
);
console.log(`wrote ${cases.length} cases for ${nodeVersion}`);
