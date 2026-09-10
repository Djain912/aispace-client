#!/usr/bin/env node

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const MCP_NAME = 'sh.aispace/mcp';
const NPM_PACKAGE = '@aispace-sh/cli';
const VERSION_PATTERN = /^\d+\.\d+\.\d+$/;

function readJSON(file) {
  return JSON.parse(fs.readFileSync(file, 'utf8'));
}

function writeJSON(file, value) {
  fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`);
}

export function stampReleaseVersion(root, version) {
  if (!VERSION_PATTERN.test(version)) {
    throw new Error(`version must look like 0.4.1, got ${JSON.stringify(version)}`);
  }

  const packagePath = path.join(root, 'npm', 'package.json');
  const lockPath = path.join(root, 'npm', 'package-lock.json');
  const serverPath = path.join(root, 'server.json');
  const pkg = readJSON(packagePath);
  const lock = readJSON(lockPath);
  const server = readJSON(serverPath);

  if (pkg.name !== NPM_PACKAGE || pkg.mcpName !== MCP_NAME) {
    throw new Error('npm package name or mcpName does not match the release contract');
  }
  if (lock.name !== NPM_PACKAGE || lock.packages?.['']?.name !== NPM_PACKAGE) {
    throw new Error('npm lockfile does not match the release package');
  }
  if (server.name !== MCP_NAME) {
    throw new Error('server.json name does not match npm mcpName');
  }
  const npmEntries = server.packages?.filter(
    (entry) => entry.registryType === 'npm' && entry.identifier === NPM_PACKAGE,
  );
  if (npmEntries?.length !== 1) {
    throw new Error('server.json must contain exactly one @aispace-sh/cli npm package');
  }

  pkg.version = version;
  lock.version = version;
  lock.packages[''].version = version;
  server.version = version;
  npmEntries[0].version = version;

  writeJSON(packagePath, pkg);
  writeJSON(lockPath, lock);
  writeJSON(serverPath, server);
}

const scriptPath = fileURLToPath(import.meta.url);
if (process.argv[1] && path.resolve(process.argv[1]) === scriptPath) {
  const version = process.argv[2];
  if (!version || process.argv.length !== 3) {
    console.error('usage: node scripts/stamp-release-version.mjs <version>');
    process.exit(2);
  }
  try {
    stampReleaseVersion(path.resolve(path.dirname(scriptPath), '..'), version);
  } catch (error) {
    console.error(`could not stamp release metadata: ${error.message}`);
    process.exit(1);
  }
}
