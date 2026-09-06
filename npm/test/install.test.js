'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { expectedChecksum, platformAsset } = require('../scripts/install');

test('maps supported platforms to release assets', () => {
  assert.equal(platformAsset('darwin', 'arm64'), 'aispace_darwin_arm64');
  assert.equal(platformAsset('linux', 'x64'), 'aispace_linux_amd64');
  assert.throws(() => platformAsset('win32', 'x64'), /unsupported platform/);
});

test('extracts only the checksum for the requested asset', () => {
  const hash = 'a'.repeat(64);
  assert.equal(expectedChecksum(`${'b'.repeat(64)}  other\n${hash}  aispace_linux_amd64\n`, 'aispace_linux_amd64'), hash);
  assert.throws(() => expectedChecksum('', 'aispace_linux_amd64'), /checksum missing/);
});
