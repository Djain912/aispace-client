'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { binaryName, expectedChecksum, platformAsset } = require('../scripts/install');

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
