#!/usr/bin/env node

import fs from 'node:fs';
import path from 'node:path';

const supported = new Set(['.md', '.markdown', '.mdx', '.json']);
const markdown = new Set(['.md', '.markdown', '.mdx']);
const knownSections = new Set(['main', 'guides', 'architecture', 'reference']);
const repoRoot = path.resolve(process.argv[2] || '.');
const docsRoot = path.join(repoRoot, 'docs');
const indexPath = path.join(docsRoot, 'index.json');
const errors = [];
const warnings = [];

function isSupported(file) {
  return supported.has(path.extname(file).toLowerCase());
}

function isMarkdown(file) {
  return markdown.has(path.extname(file).toLowerCase());
}

function walk(dir, base = '', include = isSupported) {
  const files = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.name.startsWith('.')) continue;
    const relative = base ? `${base}/${entry.name}` : entry.name;
    if (entry.isDirectory()) files.push(...walk(path.join(dir, entry.name), relative, include));
    if (entry.isFile() && include(entry.name) && entry.name !== 'index.json') files.push(relative);
  }
  return files;
}

function resolveSource(source) {
  for (const candidate of [path.join(docsRoot, source), path.join(repoRoot, source)]) {
    if (fs.existsSync(candidate) && fs.statSync(candidate).isFile()) return candidate;
  }
  return null;
}

function normalizeSource(source) {
  const relative = source.replace(/^\.\//, '');
  return relative.startsWith('docs/') ? relative.slice('docs/'.length) : relative;
}

function expandWildcard(section, source) {
  const folder = source === '*' ? section : source.slice(0, -2);
  const absolute = path.join(docsRoot, folder);
  if (!fs.existsSync(absolute) || !fs.statSync(absolute).isDirectory()) {
    errors.push(`wildcard ${source} in ${section} points to missing docs/${folder}`);
    return [];
  }
  const files = walk(absolute, folder);
  if (files.length === 0) errors.push(`wildcard ${source} in ${section} is empty`);
  return files;
}

if (!fs.existsSync(indexPath)) {
  console.error(`missing ${indexPath}`);
  process.exit(1);
}

let index;
try {
  index = JSON.parse(fs.readFileSync(indexPath, 'utf8'));
} catch (error) {
  console.error(`invalid ${indexPath}: ${error.message}`);
  process.exit(1);
}
if (!index || typeof index !== 'object' || Array.isArray(index)) {
  console.error('docs/index.json must be an object');
  process.exit(1);
}

const reachable = new Set();
function addReachable(source) {
  const normalized = normalizeSource(source);
  if (reachable.has(normalized)) errors.push(`duplicate entry: ${normalized}`);
  else reachable.add(normalized);
}

for (const [section, rawEntries] of Object.entries(index)) {
  if (!knownSections.has(section)) warnings.push(`unknown section ${section}`);
  const entries = Array.isArray(rawEntries) ? rawEntries : [rawEntries];
  if (!entries.every((entry) => typeof entry === 'string')) {
    errors.push(`section ${section} must contain a path or array of paths`);
    continue;
  }
  for (const source of entries) {
    if (/^https?:\/\//i.test(source)) continue;
    const normalized = normalizeSource(source);
    if (normalized === '*' || normalized.endsWith('/*')) {
      for (const file of expandWildcard(section, normalized)) addReachable(file);
      continue;
    }
    if (!isSupported(normalized)) errors.push(`${source} has an unsupported extension`);
    if (!resolveSource(normalized)) errors.push(`dead entry: ${source}`);
    else addReachable(normalized);
  }
}

const onDisk = new Set(walk(docsRoot, '', isMarkdown));
if (fs.existsSync(path.join(repoRoot, 'README.md'))) onDisk.add('README.md');
for (const orphan of [...onDisk].filter((file) => !reachable.has(file)).sort()) {
  errors.push(`orphan document: ${orphan}`);
}

console.log(`index.json: ${errors.length ? 'invalid' : 'valid'} — ${reachable.size} indexed / ${onDisk.size} on disk`);
for (const warning of warnings) console.warn(`warning: ${warning}`);
for (const error of errors) console.error(`error: ${error}`);
process.exit(errors.length ? 1 : 0);
