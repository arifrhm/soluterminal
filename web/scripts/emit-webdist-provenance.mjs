#!/usr/bin/env node

import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const webRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const repoRoot = path.resolve(webRoot, '..');
const distRoot = path.join(webRoot, 'dist');

function releaseVersion() {
  try {
    const conf = JSON.parse(readFileSync(path.join(repoRoot, 'desktop/src-tauri/tauri.conf.json'), 'utf8'));
    return typeof conf.version === 'string' && conf.version ? conf.version : 'dev';
  } catch {
    return 'dev';
  }
}

function sourceSha() {
  const fromEnv = process.env.SOLU_SOURCE_SHA?.trim();
  if (fromEnv) return fromEnv;
  try {
    return execFileSync('git', ['rev-parse', 'HEAD'], {
      cwd: repoRoot,
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }).trim() || 'unknown';
  } catch {
    return 'unknown';
  }
}

export function distFiles(dir, prefix = '') {
  const files = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (relative === 'webdist.json') continue;
    if (entry.isDirectory()) files.push(...distFiles(path.join(dir, entry.name), relative));
    else if (entry.isFile()) files.push(relative);
  }
  return files.sort();
}

export function computeDistDigest(root) {
  const hash = createHash('sha256');
  for (const relative of distFiles(root)) {
    hash.update(relative, 'utf8');
    hash.update(readFileSync(path.join(root, ...relative.split('/'))));
  }
  return hash.digest('hex');
}

export function emitWebdistProvenance({ root = distRoot, version = releaseVersion(), sha = sourceSha() } = {}) {
  if (!/^(?:[0-9a-fA-F]+|unknown)$/.test(sha)) {
    throw new Error(`SOLU_SOURCE_SHA must be hexadecimal or "unknown", got ${JSON.stringify(sha)}`);
  }
  const digest = computeDistDigest(root);
  const marker = `solu-webdist v=${version} src=${sha} sha256=${digest}`;
  const provenance = { marker, webVersion: version, sourceSha: sha, digest };
  const bytes = `${JSON.stringify(provenance)}\n`;
  writeFileSync(path.join(root, 'webdist.json'), bytes, 'utf8');
  return { ...provenance, bytes };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  emitWebdistProvenance();
}
