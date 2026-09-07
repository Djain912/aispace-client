'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const { binaryName, expectedChecksum, installLocalBinary, platformAsset, temporaryPath, validateDownloadURL } = require('../scripts/install');

test('maps supported platforms to release assets', () => {
  assert.equal(platformAsset('darwin', 'arm64'), 'aispace_darwin_arm64');
  assert.equal(platformAsset('linux', 'x64'), 'aispace_linux_amd64');
  assert.equal(platformAsset('win32', 'x64'), 'aispace_windows_amd64.exe');
  assert.equal(platformAsset('win32', 'arm64'), 'aispace_windows_arm64.exe');
  assert.throws(() => platformAsset('freebsd', 'x64'), /unsupported platform: freebsd\/x64/);
  assert.throws(() => platformAsset('linux', 'ia32'), /unsupported platform: linux\/ia32/);
});

test('uses the Windows executable suffix only on Windows', () => {
  assert.equal(binaryName('darwin'), 'aispace');
  assert.equal(binaryName('linux'), 'aispace');
  assert.equal(binaryName('win32'), 'aispace.exe');
});

test('extracts only the checksum for the requested asset', () => {
  const hash = 'a'.repeat(64);
  assert.equal(expectedChecksum(`${'b'.repeat(64)}  other\n${hash}  aispace_linux_amd64\n`, 'aispace_linux_amd64'), hash);
  assert.throws(() => expectedChecksum('', 'aispace_linux_amd64'), /checksum missing/);
});

test('accepts only trusted HTTPS release download URLs', () => {
  assert.equal(validateDownloadURL('https://github.com/aispace-sh/aispace-client/releases').hostname, 'github.com');
  assert.equal(validateDownloadURL('https://release-assets.githubusercontent.com/object').hostname, 'release-assets.githubusercontent.com');
  assert.throws(() => validateDownloadURL('http://github.com/file'), /untrusted download URL/);
  assert.throws(() => validateDownloadURL('https://githubusercontent.com.evil.example/file'), /untrusted download URL/);
  assert.throws(() => validateDownloadURL('https://example.com/file'), /untrusted download URL/);
});

test('creates installer temporary files beside the final binary', () => {
  const target = path.join('/tmp', 'package', 'bin', 'aispace');
  const temp = temporaryPath(target);
  assert.equal(path.dirname(temp), path.dirname(target));
  assert.match(path.basename(temp), /^\.aispace\.\d+\.[a-f0-9]{16}\.tmp$/);
});

test('atomically replaces a binary without leaving its temporary file', (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'aispace-npm-install-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const source = path.join(dir, 'source');
  const target = path.join(dir, 'aispace');
  fs.writeFileSync(source, 'new binary');
  fs.writeFileSync(target, 'old binary');

  installLocalBinary(source, target);

  assert.equal(fs.readFileSync(target, 'utf8'), 'new binary');
  assert.deepEqual(fs.readdirSync(dir).sort(), ['aispace', 'source']);
});
