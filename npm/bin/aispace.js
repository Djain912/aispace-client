#!/usr/bin/env node
'use strict';

const { spawnSync } = require('node:child_process');
const path = require('node:path');
const { binaryName } = require('../scripts/install');

const binary = path.join(__dirname, binaryName());
const result = spawnSync(binary, process.argv.slice(2), { stdio: 'inherit' });
if (result.error) {
  console.error(`aispace: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status ?? 1);
