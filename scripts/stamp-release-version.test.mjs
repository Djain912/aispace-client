import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

import { stampReleaseVersion } from './stamp-release-version.mjs';

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'aispace-release-version-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.mkdirSync(path.join(root, 'npm'));
  fs.writeFileSync(path.join(root, 'npm', 'package.json'), JSON.stringify({
    name: '@aispace-sh/cli', version: '0.0.0', mcpName: 'sh.aispace/mcp',
  }));
  fs.writeFileSync(path.join(root, 'npm', 'package-lock.json'), JSON.stringify({
    name: '@aispace-sh/cli', version: '0.0.0', packages: { '': { name: '@aispace-sh/cli', version: '0.0.0' } },
  }));
  fs.writeFileSync(path.join(root, 'server.json'), JSON.stringify({
    name: 'sh.aispace/mcp', version: '0.0.0', packages: [
      { registryType: 'npm', identifier: '@aispace-sh/cli', version: '0.0.0' },
    ],
  }));
  return root;
}

test('stamps npm, lockfile, and MCP metadata with one release version', (t) => {
  const root = fixture(t);
  stampReleaseVersion(root, '0.4.1');

  const pkg = JSON.parse(fs.readFileSync(path.join(root, 'npm', 'package.json')));
  const lock = JSON.parse(fs.readFileSync(path.join(root, 'npm', 'package-lock.json')));
  const server = JSON.parse(fs.readFileSync(path.join(root, 'server.json')));
  assert.equal(pkg.version, '0.4.1');
  assert.equal(lock.version, '0.4.1');
  assert.equal(lock.packages[''].version, '0.4.1');
  assert.equal(server.version, '0.4.1');
  assert.equal(server.packages[0].version, '0.4.1');
});

test('rejects invalid versions and mismatched MCP identities', (t) => {
  const root = fixture(t);
  assert.throws(() => stampReleaseVersion(root, 'v0.4.1'), /version must look like/);

  const packagePath = path.join(root, 'npm', 'package.json');
  const pkg = JSON.parse(fs.readFileSync(packagePath));
  pkg.mcpName = 'io.example/wrong';
  fs.writeFileSync(packagePath, JSON.stringify(pkg));
  assert.throws(() => stampReleaseVersion(root, '0.4.1'), /does not match/);
});
