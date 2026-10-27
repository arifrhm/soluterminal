// Lightweight, dependency-free per-line syntax highlighter used by the native
// renderer (markdown code blocks + diffs) AND the FileTile viewer. Per-language
// keyword sets plus heuristic function/type detection give an IDE-ish read
// without a grammar engine or a heavy dependency — which matters because the
// native renderer re-runs this on every streaming frame, so it must stay cheap
// (single forward pass, O(line length)). Per-line only: constructs that SPAN
// lines (block comments, multi-line strings) aren't tracked across lines — a
// deliberate trade for the streaming budget. The tokenizer is LOSSLESS: joining
// the token texts reproduces the input line exactly.

export type Lang =
  | 'json'
  | 'yaml'
  | 'toml'
  | 'ts'
  | 'js'
  | 'go'
  | 'python'
  | 'rust'
  | 'shell'
  | 'sql'
  | 'c'
  | 'css'
  | 'code' // generic code fallback (ruby/php/java/… and unknown code-ish files)
  | 'plain';

export type TokClass = '' | 'str' | 'num' | 'comment' | 'key' | 'kw' | 'bool' | 'fn' | 'type';

export interface Tok {
  text: string;
  cls: TokClass;
}

// Literal constants, matched case-insensitively so Python's True/False/None and
// JS's true/false/null/undefined all land as `bool`.
const BOOLS = new Set(['true', 'false', 'null', 'nil', 'none', 'undefined', 'nan', 'infinity']);

// Per-language keyword lists (space-separated for brevity → Set below). Keeping
// them separate avoids cross-contamination: Go's `chan`/`defer` aren't JS
// keywords, Python's `elif`/`lambda` aren't Go's. `generic` is a broad union for
// code-ish files we can't pin to a specific language.
const KW = {
  js: 'abstract as async await break case catch class const continue debugger declare default delete do else enum export extends finally for from function get if implements import in instanceof interface is keyof let namespace new of override private protected public readonly return satisfies set static super switch this throw try type typeof void while with yield',
  go: 'break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var',
  python:
    'and as assert async await break case class continue def del elif else except finally for from global if import in is lambda match nonlocal not or pass raise return try while with yield',
  rust: 'as async await break const continue crate dyn else enum extern fn for if impl in let loop match mod move mut pub ref return static struct super trait type unsafe use where while',
  shell:
    'alias case declare do done elif else esac export fi for function if in local readonly return select set source then unset until while',
  c: 'auto break case char const continue default do double else enum extern float for goto if inline int long register return short signed sizeof static struct switch typedef union unsigned void volatile while',
  sql: 'add all alter and as asc between by check column constraint create default delete desc distinct drop exists foreign from group having in index inner insert into is join key left like limit not null offset on or order outer primary references right select set table union unique update values view where',
  generic:
    'abstract and as async await break case catch chan class const continue def default defer del elif else enum export extends fallthrough finally fn for from func go goto if impl implements import in interface is lambda let loop map match mod mut new not of or override package pass private protected pub public range readonly ref return select self static struct super switch this throw trait try type typeof use var void while with yield',
} as const;

const toSet = (s: string): Set<string> => new Set(s.split(' '));
const KW_SETS: Partial<Record<Lang, Set<string>>> = {
  js: toSet(KW.js),
  ts: toSet(KW.js),
  go: toSet(KW.go),
  python: toSet(KW.python),
  rust: toSet(KW.rust),
  shell: toSet(KW.shell),
  c: toSet(KW.c),
  sql: toSet(KW.sql),
  code: toSet(KW.generic),
};

// Languages that get code-style treatment (function/type detection). css gets
// function detection too (rgb()/calc()) but not type detection.
const CODE_LANGS = new Set<Lang>(['ts', 'js', 'go', 'python', 'rust', 'shell', 'sql', 'c', 'code']);
// Languages where `/* … */` is a comment (single-line span only here).
const BLOCK_COMMENT_LANGS = new Set<Lang>(['ts', 'js', 'go', 'rust', 'c', 'css', 'code']);
// Languages with `@name` tokens (TS/JS decorators, CSS at-rules, Java/C# annotations).
const AT_LANGS = new Set<Lang>(['ts', 'js', 'css', 'code']);

export function detectLang(path: string): Lang {
  const base = path.toLowerCase().split(/[\\/]/).pop() || '';
  if (base === 'dockerfile' || base === 'makefile') return 'shell';
  if (base === '.gitignore' || base === '.dockerignore' || base === '.npmrc') return 'shell';
  const ext = base.includes('.') ? base.slice(base.lastIndexOf('.') + 1) : '';
  switch (ext) {
    case 'json':
    case 'json5':
    case 'jsonc':
      return 'json';
    case 'yaml':
    case 'yml':
      return 'yaml';
    case 'toml':
      return 'toml';
    case 'ts':
    case 'tsx':
    case 'mts':
    case 'cts':
      return 'ts';
    case 'js':
    case 'jsx':
    case 'mjs':
    case 'cjs':
      return 'js';
    case 'go':
      return 'go';
    case 'py':
    case 'pyi':
      return 'python';
    case 'rs':
      return 'rust';
    case 'sh':
    case 'bash':
    case 'zsh':
    case 'fish':
      return 'shell';
    case 'sql':
      return 'sql';
    case 'c':
    case 'h':
    case 'cc':
    case 'cpp':
    case 'hpp':
    case 'cxx':
      return 'c';
    case 'css':
    case 'scss':
    case 'less':
      return 'css';
    case 'rb':
    case 'php':
    case 'java':
    case 'cs':
    case 'swift':
    case 'kt':
    case 'lua':
      return 'code';
    default:
      return 'plain';
  }
}

const isDigit = (c: string) => c >= '0' && c <= '9';
const isWordStart = (c: string) =>
  (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c === '_' || c === '$';
const isWord = (c: string) => isWordStart(c) || isDigit(c);
const isHex = (c: string) =>
  isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F');

// PascalCase type/class/component name: starts uppercase and contains a
// lowercase letter — so ALL_CAPS constants and single-letter generics (`T`)
// stay plain rather than being mis-typed.
function isTypeName(word: string): boolean {
  if (word.length < 2) return false;
  if (word[0] < 'A' || word[0] > 'Z') return false;
  return word !== word.toUpperCase();
}

// Comment prefixes per language. JSON has none; css uses only `/* */` (handled
// inline); `code` covers both `//` and `#` since it's the can't-pin fallback.
function commentPrefixes(lang: Lang): string[] {
  switch (lang) {
    case 'python':
    case 'shell':
    case 'yaml':
    case 'toml':
      return ['#'];
    case 'sql':
      return ['--'];
    case 'ts':
    case 'js':
    case 'go':
    case 'rust':
    case 'c':
      return ['//'];
    case 'code':
      return ['//', '#'];
    default:
      return [];
  }
}

export function tokenizeLine(line: string, lang: Lang): Tok[] {
  if (lang === 'plain' || line === '') return [{ text: line, cls: '' }];

  const toks: Tok[] = [];
  const comments = commentPrefixes(lang);
  const kw = KW_SETS[lang] ?? null;
  const isCode = CODE_LANGS.has(lang);
  const blockComment = BLOCK_COMMENT_LANGS.has(lang);
  const atKw = AT_LANGS.has(lang);
  const fnLang = isCode || lang === 'css';
  const n = line.length;
  let i = 0;
  let plainStart = 0;
  const flush = (end: number) => {
    if (end > plainStart) toks.push({ text: line.slice(plainStart, end), cls: '' });
  };
  // Emit a classified token spanning [start, end), flushing any pending plain
  // text before it. Keeps the tokenizer lossless.
  const emit = (start: number, end: number, cls: TokClass) => {
    flush(start);
    toks.push({ text: line.slice(start, end), cls });
    plainStart = end;
  };

  while (i < n) {
    const c = line[i];

    // Block comment /* … */ (single-line; an unterminated open runs to EOL).
    if (blockComment && c === '/' && line[i + 1] === '*') {
      const close = line.indexOf('*/', i + 2);
      const end = close === -1 ? n : close + 2;
      emit(i, end, 'comment');
      i = end;
      continue;
    }

    // Line comment to EOL (we're always at top level — strings are consumed
    // below before we'd reach a `#`/`//`/`--` inside one).
    let cm = '';
    for (const p of comments) {
      if (line.startsWith(p, i)) {
        cm = p;
        break;
      }
    }
    if (cm) {
      emit(i, n, 'comment');
      i = n;
      break;
    }

    // String literal: ", ' or ` with backslash escapes.
    if (c === '"' || c === "'" || c === '`') {
      let j = i + 1;
      while (j < n) {
        if (line[j] === '\\') {
          j += 2;
          continue;
        }
        if (line[j] === c) {
          j++;
          break;
        }
        j++;
      }
      // A quoted string immediately before ':' is a JSON/YAML key.
      let cls: TokClass = 'str';
      if (lang === 'json' || lang === 'yaml') {
        let k = j;
        while (k < n && line[k] === ' ') k++;
        if (line[k] === ':') cls = 'key';
      }
      emit(i, j, cls);
      i = j;
      continue;
    }

    // @decorator / @at-rule (TS decorators, CSS @media/@import, Java annotations).
    if (atKw && c === '@' && isWordStart(line[i + 1] || '')) {
      let j = i + 1;
      while (j < n && isWord(line[j])) j++;
      emit(i, j, 'kw');
      i = j;
      continue;
    }

    // CSS hex color (#rgb / #rgba / #rrggbb / #rrggbbaa).
    if (lang === 'css' && c === '#') {
      let j = i + 1;
      while (j < n && isHex(line[j])) j++;
      const len = j - (i + 1);
      if ((len === 3 || len === 4 || len === 6 || len === 8) && !isWord(line[j] || '')) {
        emit(i, j, 'num');
        i = j;
        continue;
      }
    }

    // Number (must not start mid-identifier).
    if (isDigit(c) && (i === 0 || !isWord(line[i - 1]))) {
      let j = i + 1;
      if (c === '0' && (line[j] === 'x' || line[j] === 'X')) {
        j++;
        while (j < n && (isHex(line[j]) || line[j] === '_')) j++;
      } else {
        while (j < n && (isDigit(line[j]) || line[j] === '.' || line[j] === '_')) j++;
        if (line[j] === 'e' || line[j] === 'E') {
          j++;
          if (line[j] === '+' || line[j] === '-') j++;
          while (j < n && isDigit(line[j])) j++;
        }
      }
      emit(i, j, 'num');
      i = j;
      continue;
    }

    // Identifier / bareword.
    if (isWordStart(c)) {
      let j = i;
      while (j < n && isWord(line[j])) j++;
      const word = line.slice(i, j);
      const immediate = line[j] || ''; // next char, no skip — for fn-call '('
      let k = j;
      while (k < n && line[k] === ' ') k++;
      const nextNonSpace = line[k] || ''; // for key (':' / '=') detection
      let cls: TokClass = '';
      if (BOOLS.has(word.toLowerCase())) {
        cls = 'bool';
      } else if (kw && kw.has(lang === 'sql' ? word.toLowerCase() : word)) {
        cls = 'kw';
      } else if (lang === 'css' && nextNonSpace === ':') {
        cls = 'key'; // CSS property (or pseudo) before ':'
      } else if (fnLang && immediate === '(') {
        cls = 'fn'; // call/definition: foo(
      } else if (isCode && isTypeName(word)) {
        cls = 'type'; // PascalCase type / class / component
      } else if (lang === 'yaml' && nextNonSpace === ':' && (k + 1 >= n || line[k + 1] === ' ')) {
        cls = 'key'; // yaml bareword key (but not a `http://` scheme)
      } else if (lang === 'toml' && nextNonSpace === '=') {
        cls = 'key'; // toml bareword key
      }
      if (cls) {
        flush(i);
        toks.push({ text: word, cls });
        plainStart = j;
      }
      i = j;
      continue;
    }

    i++;
  }
  flush(n);
  return toks.length ? toks : [{ text: line, cls: '' }];
}
