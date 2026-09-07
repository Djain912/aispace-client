#!/usr/bin/env node
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const https = require('node:https');
const path = require('node:path');

const REPOSITORY = 'aispace-sh/aispace-client';
const REQUEST_INACTIVITY_MS = 30_000;
const MAX_CHECKSUM_BYTES = 1024 * 1024;
const MAX_BINARY_BYTES = 256 * 1024 * 1024;

function platformAsset(platform = process.platform, arch = process.arch) {
  const os = { darwin: 'darwin', linux: 'linux', win32: 'windows' }[platform];
  const cpu = { x64: 'amd64', arm64: 'arm64' }[arch];
  if (!os || !cpu) throw new Error(`unsupported platform: ${platform}/${arch}`);
  const extension = platform === 'win32' ? '.exe' : '';
  return `aispace_${os}_${cpu}${extension}`;
}

function binaryName(platform = process.platform) {
  return platform === 'win32' ? 'aispace.exe' : 'aispace';
}

function validateDownloadURL(value) {
  const url = value instanceof URL ? value : new URL(value);
  const trustedHost = url.hostname === 'github.com' || url.hostname.endsWith('.githubusercontent.com');
  if (url.protocol !== 'https:' || !trustedHost || url.username || url.password) {
    throw new Error(`refusing untrusted download URL: ${url.origin}`);
  }
  return url;
}

function responseFor(url, redirects = 0) {
  return new Promise((resolve, reject) => {
    let parsed;
    try {
      parsed = validateDownloadURL(url);
    } catch (error) {
      reject(error);
      return;
    }
    const request = https.get(parsed, { headers: { 'User-Agent': 'aispace-cli npm installer' } }, (response) => {
      if (response.statusCode >= 300 && response.statusCode < 400 && response.headers.location) {
        response.resume();
        if (redirects >= 5) {
          reject(new Error('too many redirects'));
          return;
        }
        responseFor(new URL(response.headers.location, parsed), redirects + 1).then(resolve, reject);
        return;
      }
      if (response.statusCode !== 200) {
        response.resume();
        reject(new Error(`download failed with HTTP ${response.statusCode}`));
        return;
      }
      response.setTimeout(REQUEST_INACTIVITY_MS, () => response.destroy(new Error('download stalled')));
      resolve(response);
    });
    request.setTimeout(REQUEST_INACTIVITY_MS, () => request.destroy(new Error('download stalled')));
    request.on('error', reject);
  });
}

async function downloadBuffer(url, maxBytes = MAX_CHECKSUM_BYTES) {
  const response = await responseFor(url);
  return new Promise((resolve, reject) => {
    const chunks = [];
    let total = 0;
    response.on('data', (chunk) => {
      total += chunk.length;
      if (total > maxBytes) {
        response.destroy(new Error(`download exceeds ${maxBytes} bytes`));
        return;
      }
      chunks.push(chunk);
    });
    response.on('end', () => resolve(Buffer.concat(chunks)));
    response.on('error', reject);
  });
}

async function downloadToFile(url, destination, maxBytes = MAX_BINARY_BYTES) {
  const response = await responseFor(url);
  return new Promise((resolve, reject) => {
    const output = fs.createWriteStream(destination, { flags: 'wx', mode: 0o755 });
    const hash = crypto.createHash('sha256');
    let total = 0;
    let settled = false;
    let digest;
    const fail = (error) => {
      if (settled) return;
      settled = true;
      response.destroy();
      output.destroy();
      reject(error);
    };
    response.on('data', (chunk) => {
      total += chunk.length;
      if (total > maxBytes) {
        fail(new Error(`download exceeds ${maxBytes} bytes`));
        return;
      }
      hash.update(chunk);
    });
    response.on('error', fail);
    output.on('error', fail);
    output.on('finish', () => {
      digest = hash.digest('hex');
    });
    output.on('close', () => {
      if (settled) return;
      settled = true;
      resolve(digest);
    });
    response.pipe(output);
  });
}

function temporaryPath(target) {
  const suffix = crypto.randomBytes(8).toString('hex');
  return path.join(path.dirname(target), `.${path.basename(target)}.${process.pid}.${suffix}.tmp`);
}

function installLocalBinary(source, target) {
  const temp = temporaryPath(target);
  try {
    fs.copyFileSync(source, temp, fs.constants.COPYFILE_EXCL);
    fs.chmodSync(temp, 0o755);
    fs.renameSync(temp, target);
  } finally {
    fs.rmSync(temp, { force: true });
  }
}

function expectedChecksum(checksums, asset) {
  const line = checksums.split(/\r?\n/).find((entry) => entry.endsWith(`  ${asset}`));
  if (!line || !/^[a-f0-9]{64}  /.test(line)) throw new Error(`checksum missing for ${asset}`);
  return line.slice(0, 64);
}

async function main() {
  const pkg = require('../package.json');
  const target = path.join(__dirname, '..', 'bin', binaryName());
  if (process.env.AISPACE_NPM_BINARY) {
    installLocalBinary(process.env.AISPACE_NPM_BINARY, target);
    return;
  }
  const temp = temporaryPath(target);
  try {
    const asset = platformAsset();
    const base = `https://github.com/${REPOSITORY}/releases/download/v${pkg.version}`;
    const checksumFile = await downloadBuffer(`${base}/checksums.txt`);
    const expected = expectedChecksum(checksumFile.toString('utf8'), asset);
    const actual = await downloadToFile(`${base}/${asset}`, temp);
    if (actual !== expected) throw new Error(`checksum mismatch for ${asset}`);
    fs.renameSync(temp, target);
  } finally {
    fs.rmSync(temp, { force: true });
  }
}

if (require.main === module) {
  main().catch((error) => {
    console.error(`aispace-cli install failed: ${error.message}`);
    process.exit(1);
  });
}

module.exports = { binaryName, expectedChecksum, installLocalBinary, platformAsset, temporaryPath, validateDownloadURL };
