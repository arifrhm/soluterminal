#!/usr/bin/env node
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';
import { runtimeRoot, nodeSupportsDsh, nodeSupportsPi, verifyDeepseekConfig, verifyPiConfig, readDshProvenance, readPiProvenance, writeDshProvenance, writePiProvenance, writeManagedNpmrc, shortcutDisposition, shortcutDataDir, renderManagedShortcut } from './launch.mjs';
import { PINS } from './pins.mjs';

const [provider, ...args] = process.argv.slice(2);
if (!['glm', 'deepseek', 'pi', 'pi-glm'].includes(provider)) throw new Error('Choose glm, deepseek, pi, or pi-glm.');
const dataDir = args[0] === '--data-dir' && args[1] ? args[1] : path.join(os.homedir(), '.solu');
const versionMatches = async (file, wanted, flag = '--version') => {
  try { const result = await promisify(execFile)(process.execPath, [file, flag], { timeout: 15000 }); return result.stdout.trim() === wanted; }
  catch { return false; }
};
// Windows ships npm as npm.cmd, which spawn cannot start without a shell. Run
// the npm-cli.js bundled beside this Node instead, so no shell quoting is needed.
const npm = argv => {
  if (process.platform !== 'win32') return ['npm', argv];
  const cli = path.join(path.dirname(process.execPath), 'node_modules', 'npm', 'bin', 'npm-cli.js');
  return [process.execPath, [cli, ...argv]];
};
const run = (command, argv, cwd) => new Promise((resolve, reject) => {
  const child = spawn(command, argv, { cwd, stdio: 'inherit', shell: false });
  child.on('error', reject); child.on('exit', code => code === 0 ? resolve() : reject(new Error(`${command} exited ${code}`)));
});
const capture = (command, argv, cwd, timeoutMs = 120000) => new Promise(resolve => {
  const child = spawn(command, argv, { cwd, stdio: ['ignore', 'pipe', 'pipe'], shell: false });
  let out = '';
  child.stdout.on('data', data => { out += data; });
  child.stderr.on('data', data => { out += data; });
  const timer = setTimeout(() => { child.kill('SIGKILL'); resolve({ code: null, out: 'timed out' }); }, timeoutMs); timer.unref?.();
  child.on('error', error => { clearTimeout(timer); resolve({ code: 1, out: String(error?.message ?? error) }); });
  child.on('exit', code => { clearTimeout(timer); resolve({ code: code ?? 1, out }); });
});
// Best-effort registry signature verification (Pi-style): surface it, but do not
// block an install on a transient audit-endpoint outage.
const verifySignatures = async (prefix, label = 'DeepSeek Harness') => {
  const result = await capture(...npm(['audit', 'signatures']), prefix);
  const tail = String(result.out).split('\n').map(line => line.trim()).filter(Boolean).slice(-1)[0] || '';
  if (result.code === 0) console.log(`${label} registry signatures verified${tail ? ` (${tail})` : ''}.`);
  else console.log(`warning: registry signature verification unavailable: ${(tail || 'npm audit signatures failed').slice(0, 160)}`);
};
await fs.mkdir(runtimeRoot, { recursive: true, mode: 0o700 });
if (provider === 'deepseek') {
  // dsh's CLI is a no-op without `import.meta.main`; fail the install rather
  // than leave a launcher that hangs on a handshake that never comes.
  if (!nodeSupportsDsh()) throw new Error('DeepSeek Harness needs Node 24.2 or newer (its CLI relies on import.meta.main). Update Node and rerun the installer.');
  const prefix = path.join(runtimeRoot, 'deepseek');
  const runtime = path.join(prefix, 'node_modules/@deepseek-ai/dsh/lib/bin.js');
  const PIN = PINS.deepseek;
  await writeManagedNpmrc(prefix);
  // Reinstall when the version is wrong OR the pinned provenance is missing, so
  // an unverified package cannot ride along on a matching version string.
  let provenance = await readDshProvenance(prefix, PIN);
  if (!(await versionMatches(runtime, PIN)) || !provenance) {
    await run(...npm(['install', '--prefix', prefix, '--save-exact', '--no-audit', '--no-fund', `@deepseek-ai/dsh@${PIN}`]));
    provenance = await readDshProvenance(prefix, PIN);
  }
  if (!provenance) throw new Error('DeepSeek Harness install could not be verified against npm provenance (package-lock.json is missing or does not resolve the pinned version from registry.npmjs.org).');
  await writeDshProvenance(dataDir, provenance);
  console.log(`DeepSeek Harness ${provenance.version} provenance: ${provenance.resolved} ${provenance.integrity.slice(0, 24)}…`);
  await verifySignatures(prefix);
  // A version string does not prove the harness is usable: compose the ACP
  // profile once, offline, so a broken install fails here, not at launch.
  await verifyDeepseekConfig(runtime, path.join(dataDir, 'harness-state', 'deepseek'), process.execPath);
  console.log('DeepSeek Harness config round-trip verified (official ACP profile).');
// The pi-glm row shares the Pi runtime binary with the pi row: both install
// (and verify) the same pinned package into the same prefix. Their managed
// homes and credentials stay separate at launch time.
} else if (provider === 'pi' || provider === 'pi-glm') {
  if (process.platform === 'win32') throw new Error('Install this managed harness on your SoluTerminal WSL backend.');
  // Pi's published engines floor. It does not need install scripts, so the
  // managed install passes --ignore-scripts like Pi's own documented install.
  if (!nodeSupportsPi()) throw new Error('Pi needs Node 22.19 or newer. Update Node and rerun the installer.');
  const prefix = path.join(runtimeRoot, 'pi');
  const runtime = path.join(prefix, 'node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js');
  const PIN = PINS.pi;
  await writeManagedNpmrc(prefix);
  let provenance = await readPiProvenance(prefix, PIN);
  if (!(await versionMatches(runtime, PIN)) || !provenance) {
    await run(...npm(['install', '--prefix', prefix, '--save-exact', '--no-audit', '--no-fund', '--ignore-scripts', `@earendil-works/pi-coding-agent@${PIN}`]));
    provenance = await readPiProvenance(prefix, PIN);
  }
  if (!provenance) throw new Error('Pi install could not be verified against npm provenance (package-lock.json is missing or does not resolve the pinned version from registry.npmjs.org).');
  await writePiProvenance(dataDir, provenance);
  console.log(`Pi ${provenance.version} provenance: ${provenance.resolved} ${provenance.integrity.slice(0, 24)}…`);
  await verifySignatures(prefix, 'Pi');
  // A version string does not prove the bundle loads: boot it once, offline.
  await verifyPiConfig(runtime, path.join(dataDir, 'harness-state', 'pi'), process.execPath);
  console.log('Pi config round-trip verified (offline boot).');
} else {
  if (process.platform !== 'linux' || process.arch !== 'x64') throw new Error('The managed ZCode installer currently supports Linux x64, including WSL.');
  // Z.ai's desktop runtime omits the terminal UI and its bridge. Use the
  // matched, pinned community distribution, including its documented patches.
  const adapterRoot = path.join(runtimeRoot, 'zcode-terminal');
  await writeManagedNpmrc(adapterRoot);
  const adapterPackage = path.join(adapterRoot, 'node_modules/zcode-app-cli');
  let adapterVersion;
  try { adapterVersion = JSON.parse(await fs.readFile(path.join(adapterPackage, 'package.json'), 'utf8')).version; } catch {}
  if (adapterVersion !== '3.11.2-22') {
    await run(...npm(['install', '--prefix', adapterRoot, '--save-exact', '--no-audit', '--no-fund', 'zcode-app-cli@3.11.2-22']));
  }
  console.log('ZCode installed from the community zcode-app-cli distribution, including its ZCode runtime patches and terminal UI.');
}
// A shortcut is shared by every data dir on the machine. It may be replaced
// only when it is ours and either points at this data dir or this is the
// default data dir; a side-by-side install never repoints another dir's command.
const binDir = path.join(os.homedir(), '.local/bin');
await fs.mkdir(binDir, { recursive: true });
const shortcut = path.join(binDir, { glm: 'zcode', deepseek: 'deepseek', pi: 'pi', 'pi-glm': 'pi-glm' }[provider]);
const launcher = fileURLToPath(new URL('./launch.mjs', import.meta.url));
const resolver = fileURLToPath(new URL('./harness-shortcut.sh', import.meta.url));
const script = renderManagedShortcut({ resolver, node: process.execPath, launcher, provider, dataDir });
let existing = null;
try { existing = await fs.readFile(shortcut, 'utf8'); } catch (error) { if (error.code !== 'ENOENT') throw error; }
const disposition = shortcutDisposition(existing, dataDir);
if (disposition === 'create' || disposition === 'replace') { await fs.writeFile(shortcut, script, { mode: 0o700 }); await fs.chmod(shortcut, 0o700); }
else if (disposition === 'retain') console.log(`Existing ${shortcut} retained. Use the launch button in Credentials.`);
else console.log(`Existing ${shortcut} left as-is (owned by ${shortcutDataDir(existing) || 'another data dir'}). This install keeps its launcher at ${launcher}.`);
console.log(`${({ glm: 'ZCode', deepseek: 'DeepSeek Harness', pi: 'Pi', 'pi-glm': 'Pi (GLM)' })[provider]} installed. Add/select an API account in SoluTerminal → Credentials, then launch it.`);
