import { describe, it, expect } from 'vitest';
import { detectLang, tokenizeLine, type Lang } from './highlight';

describe('detectLang', () => {
  it('maps known extensions and filenames to specific languages', () => {
    expect(detectLang('/repo/config.json')).toBe('json');
    expect(detectLang('docker-compose.yml')).toBe('yaml');
    expect(detectLang('settings.yaml')).toBe('yaml');
    expect(detectLang('Cargo.toml')).toBe('toml');
    expect(detectLang('/repo/daemon/main.go')).toBe('go');
    expect(detectLang('web/src/App.tsx')).toBe('ts');
    expect(detectLang('script.mjs')).toBe('js');
    expect(detectLang('train.py')).toBe('python');
    expect(detectLang('lib.rs')).toBe('rust');
    expect(detectLang('publish.sh')).toBe('shell');
    expect(detectLang('Dockerfile')).toBe('shell');
    expect(detectLang('Makefile')).toBe('shell');
    expect(detectLang('query.sql')).toBe('sql');
    expect(detectLang('main.c')).toBe('c');
    expect(detectLang('styles.css')).toBe('css');
    expect(detectLang('legacy.rb')).toBe('code');
    expect(detectLang('notes.txt')).toBe('plain');
    expect(detectLang('README')).toBe('plain');
  });
});

describe('tokenizeLine', () => {
  it('plain language returns a single untouched token', () => {
    expect(tokenizeLine('anything # here', 'plain')).toEqual([{ text: 'anything # here', cls: '' }]);
  });

  it('json: quoted key, string value, number', () => {
    const t = tokenizeLine('  "name": "solud", "port": 8080', 'json');
    expect(t.filter((x) => x.cls === 'key').map((x) => x.text)).toEqual(['"name"', '"port"']);
    expect(t.some((x) => x.cls === 'str' && x.text === '"solud"')).toBe(true);
    expect(t.some((x) => x.cls === 'num' && x.text === '8080')).toBe(true);
  });

  it('code comment runs to EOL but a #/// inside a string is not a comment', () => {
    const t = tokenizeLine('x := "a // b # c" // tail', 'code');
    expect(t.some((x) => x.cls === 'str' && x.text === '"a // b # c"')).toBe(true);
    expect(t.some((x) => x.cls === 'comment' && x.text === '// tail')).toBe(true);
  });

  it('yaml: bareword key, but a URL scheme is not a key', () => {
    const t = tokenizeLine('url: http://example.com # c', 'yaml');
    expect(t.some((x) => x.cls === 'key' && x.text === 'url')).toBe(true);
    expect(t.some((x) => x.cls === 'key' && x.text === 'http')).toBe(false);
    expect(t.some((x) => x.cls === 'comment' && x.text === '# c')).toBe(true);
  });

  it('booleans/null are flagged in any non-plain language (incl. Python case)', () => {
    expect(tokenizeLine('enabled: true', 'yaml').some((x) => x.cls === 'bool' && x.text === 'true')).toBe(true);
    expect(tokenizeLine('return null', 'ts').some((x) => x.cls === 'bool' && x.text === 'null')).toBe(true);
    expect(tokenizeLine('x = None', 'python').some((x) => x.cls === 'bool' && x.text === 'None')).toBe(true);
  });

  it('hex and float numbers parse without swallowing trailing words', () => {
    const hex = tokenizeLine('color = 0xFF00AA', 'toml');
    expect(hex.some((x) => x.cls === 'num' && x.text === '0xFF00AA')).toBe(true);
    const flt = tokenizeLine('ratio: 1.5e3 ok', 'yaml');
    expect(flt.some((x) => x.cls === 'num' && x.text === '1.5e3')).toBe(true);
    expect(flt.map((t) => t.text).join('')).toBe('ratio: 1.5e3 ok');
  });

  it('uses PER-LANGUAGE keyword sets (no cross-contamination)', () => {
    // Go keywords highlight in Go…
    const go = tokenizeLine('func send(ch chan int) {', 'go');
    expect(go.some((x) => x.cls === 'kw' && x.text === 'func')).toBe(true);
    expect(go.some((x) => x.cls === 'kw' && x.text === 'chan')).toBe(true);
    // …but `chan` is NOT a keyword in Python, and `elif` is NOT one in Go.
    expect(tokenizeLine('chan = 3', 'python').some((x) => x.cls === 'kw')).toBe(false);
    expect(tokenizeLine('elif = 3', 'go').some((x) => x.cls === 'kw')).toBe(false);
    // Python + TS each get their own.
    expect(tokenizeLine('elif ok:', 'python').some((x) => x.cls === 'kw' && x.text === 'elif')).toBe(true);
    expect(tokenizeLine('interface Foo {', 'ts').some((x) => x.cls === 'kw' && x.text === 'interface')).toBe(true);
  });

  it('detects function-call names (ident immediately before "(")', () => {
    const t = tokenizeLine('result := compute(x, y)', 'go');
    expect(t.some((x) => x.cls === 'fn' && x.text === 'compute')).toBe(true);
    const m = tokenizeLine('obj.doThing()', 'ts');
    expect(m.some((x) => x.cls === 'fn' && x.text === 'doThing')).toBe(true);
    expect(m.some((x) => x.cls === 'fn' && x.text === 'obj')).toBe(false);
  });

  it('detects PascalCase type/component names but not ALL_CAPS constants', () => {
    const t = tokenizeLine('let v: HttpClient', 'ts');
    expect(t.some((x) => x.cls === 'type' && x.text === 'HttpClient')).toBe(true);
    // ALL_CAPS stays plain (a constant, not a type).
    expect(tokenizeLine('const MAX_SIZE = 9', 'ts').some((x) => x.cls === 'type')).toBe(false);
    // A PascalCase name CALLED like a function is `fn`, not `type` (fn wins).
    expect(tokenizeLine('render(<App />)', 'ts').some((x) => x.cls === 'fn' && x.text === 'render')).toBe(true);
  });

  it('css: property keys, hex colors, at-rules, function values', () => {
    const decl = tokenizeLine('  background: #aabbcc;', 'css');
    expect(decl.some((x) => x.cls === 'key' && x.text === 'background')).toBe(true);
    expect(decl.some((x) => x.cls === 'num' && x.text === '#aabbcc')).toBe(true);
    expect(tokenizeLine('@media (min-width: 800px) {', 'css').some((x) => x.cls === 'kw' && x.text === '@media')).toBe(true);
    expect(tokenizeLine('width: calc(100% - 8px);', 'css').some((x) => x.cls === 'fn' && x.text === 'calc')).toBe(true);
  });

  it('handles a single-line /* block comment */', () => {
    const t = tokenizeLine('const x = 1; /* inline note */ const y = 2;', 'ts');
    expect(t.some((x) => x.cls === 'comment' && x.text === '/* inline note */')).toBe(true);
    // code after the closed comment still tokenizes.
    expect(t.some((x) => x.cls === 'kw' && x.text === 'const')).toBe(true);
  });

  it('is lossless: joining token texts reproduces the line exactly', () => {
    const cases: Array<[string, Lang]> = [
      ['  "a": 1, // x', 'json'],
      ['key: value # comment', 'yaml'],
      ['func f(x int) bool { return x > 0 } // ok', 'go'],
      ['def greet(name: str) -> None:  # py', 'python'],
      ['const C = new HttpClient(); /* c */', 'ts'],
      ['  .btn:hover { color: #fff; width: calc(1px + 2em); }', 'css'],
      ['SELECT id FROM users WHERE n = 1 -- sql', 'sql'],
      ['title = "héllo ☃"', 'toml'],
      ['', 'json'],
      ['   ', 'go'],
      ['no-special-tokens-here', 'yaml'],
    ];
    for (const [line, lang] of cases) {
      expect(tokenizeLine(line, lang).map((t) => t.text).join('')).toBe(line);
    }
  });
});
