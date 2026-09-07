import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const validator = path.join(path.dirname(fileURLToPath(import.meta.url)), 'validate-index-json.mjs');

function fixture(index, files) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'aispace-doc-index-'));
  fs.mkdirSync(path.join(root, 'docs'), { recursive: true });
  fs.writeFileSync(path.join(root, 'docs', 'index.json'), JSON.stringify(index));
  for (const [name, content] of Object.entries(files)) {
    const target = path.join(root, name);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, content);
  }
  return root;
}

function validate(root) {
  return spawnSync(process.execPath, [validator, root], { encoding: 'utf8' });
}

test('normalizes entries prefixed with docs/', (t) => {
  const root = fixture(
    { main: ['README.md'], guides: ['docs/GUIDE.md'] },
    { 'README.md': '# Main', 'docs/GUIDE.md': '# Guide' },
  );
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));

  const result = validate(root);
  assert.equal(result.status, 0, result.stderr);
});

test('does not treat JSON metadata as a document', (t) => {
  const root = fixture({ main: ['README.md'] }, { 'README.md': '# Main', 'docs/search.json': '{}' });
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));

  const result = validate(root);
  assert.equal(result.status, 0, result.stderr);
});

test('accepts an explicitly indexed JSON API reference', (t) => {
  const root = fixture(
    { main: ['README.md'], reference: ['reference/api.json'] },
    { 'README.md': '# Main', 'docs/reference/api.json': '{}' },
  );
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));

  const result = validate(root);
  assert.equal(result.status, 0, result.stderr);
});

for (const entries of [
  ['guides/guide.md', 'guides/*'],
  ['guides/*', 'guides/guide.md'],
]) {
  test(`detects overlapping entries in order: ${entries.join(', ')}`, (t) => {
    const root = fixture(
      { main: ['README.md'], guides: entries },
      { 'README.md': '# Main', 'docs/guides/guide.md': '# Guide' },
    );
    t.after(() => fs.rmSync(root, { recursive: true, force: true }));

    const result = validate(root);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /duplicate entry: guides\/guide\.md/);
  });
}
