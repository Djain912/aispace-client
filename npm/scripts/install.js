#!/usr/bin/env node
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const https = require('node:https');
const path = require('node:path');

const REPOSITORY = 'aispace-sh/aispace-client';

function platformAsset(platform = process.platform, arch = process.arch) {
  const os = { darwin: 'darwin', linux: 'linux' }[platform];
  const cpu = { x64: 'amd64', arm64: 'arm64' }[arch];
  if (!os || !cpu) throw new Error(`unsupported platform: ${platform}/${arch}`);
  return `aispace_${os}_${cpu}`;
}

function download(url, redirects = 0) {
  if (redirects > 5) return Promise.reject(new Error('too many redirects'));
  return new Promise((resolve, reject) => {
    https.get(url, { headers: { 'User-Agent': 'aispace-cli npm installer' } }, (response) => {
      if (response.statusCode >= 300 && response.statusCode < 400 && response.headers.location) {
        response.resume();
        download(new URL(response.headers.location, url), redirects + 1).then(resolve, reject);
        return;
      }
      if (response.statusCode !== 200) {
        response.resume();
        reject(new Error(`download failed with HTTP ${response.statusCode}`));
        return;
      }
      const chunks = [];
      response.on('data', (chunk) => chunks.push(chunk));
      response.on('end', () => resolve(Buffer.concat(chunks)));
      response.on('error', reject);
    }).on('error', reject);
  });
}

function expectedChecksum(checksums, asset) {
  const line = checksums.split(/\r?\n/).find((entry) => entry.endsWith(`  ${asset}`));
  if (!line || !/^[a-f0-9]{64}  /.test(line)) throw new Error(`checksum missing for ${asset}`);
  return line.slice(0, 64);
}

async function main() {
  const pkg = require('../package.json');
  const target = path.join(__dirname, '..', 'bin', 'aispace');
  if (process.env.AISPACE_NPM_BINARY) {
    fs.copyFileSync(process.env.AISPACE_NPM_BINARY, target);
    fs.chmodSync(target, 0o755);
    return;
  }
  const asset = platformAsset();
  const base = `https://github.com/${REPOSITORY}/releases/download/v${pkg.version}`;
  const [binary, checksumFile] = await Promise.all([
    download(`${base}/${asset}`),
    download(`${base}/checksums.txt`),
  ]);
  const actual = crypto.createHash('sha256').update(binary).digest('hex');
  const expected = expectedChecksum(checksumFile.toString('utf8'), asset);
  if (actual !== expected) throw new Error(`checksum mismatch for ${asset}`);
  fs.writeFileSync(target, binary, { mode: 0o755 });
}

if (require.main === module) {
  main().catch((error) => {
    console.error(`aispace-cli install failed: ${error.message}`);
    process.exit(1);
  });
}

module.exports = { expectedChecksum, platformAsset };
