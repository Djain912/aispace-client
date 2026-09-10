#!/usr/bin/env node

import { spawn } from 'node:child_process';
import os from 'node:os';

const command = process.argv[2] || 'aispace';
const expectedTools = [
  'aispace_create_link',
  'aispace_delete_file',
  'aispace_download_file',
  'aispace_get_file',
  'aispace_get_quota',
  'aispace_list_files',
  'aispace_list_links',
  'aispace_revoke_link',
  'aispace_upload_file',
  'aispace_whoami',
];

const child = spawn(command, ['mcp', 'serve'], {
  env: {
    ...process.env,
    AISPACE_CONFIG: `${os.tmpdir()}/aispace-mcp-smoke-missing-config.json`,
    AISPACE_ALLOWED_ROOTS: os.tmpdir(),
    AISPACE_KEY: 'ask_smoke_fixture_do_not_use',
  },
  stdio: ['pipe', 'pipe', 'pipe'],
  shell: process.platform === 'win32',
  windowsHide: true,
});

let stderr = '';
let stdoutBuffer = '';
let initialized = false;
let listed = false;

function send(value) {
  child.stdin.write(`${JSON.stringify(value)}\n`);
}

const timeout = setTimeout(() => {
  child.kill();
  console.error('MCP smoke test timed out');
  process.exitCode = 1;
}, 10_000);

child.stderr.setEncoding('utf8');
child.stderr.on('data', (chunk) => { stderr += chunk; });
child.stdout.setEncoding('utf8');
child.stdout.on('data', (chunk) => {
  stdoutBuffer += chunk;
  for (;;) {
    const newline = stdoutBuffer.indexOf('\n');
    if (newline < 0) break;
    const line = stdoutBuffer.slice(0, newline);
    stdoutBuffer = stdoutBuffer.slice(newline + 1);
    if (!line) continue;
    const message = JSON.parse(line);
    if (message.id === 1) {
      assert(message.result?.serverInfo?.name === 'aispace', 'unexpected MCP server identity');
      assert(message.result?.capabilities?.tools, 'MCP tools capability missing');
      initialized = true;
      send({ jsonrpc: '2.0', method: 'notifications/initialized', params: {} });
      send({ jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} });
    } else if (message.id === 2) {
      const names = message.result?.tools?.map((tool) => tool.name).sort();
      assert(JSON.stringify(names) === JSON.stringify(expectedTools), 'unexpected MCP tool catalog');
      listed = true;
      child.stdin.end();
    }
  }
});

child.on('error', (error) => {
  clearTimeout(timeout);
  console.error(`could not launch ${command}: ${error.message}`);
  process.exitCode = 1;
});
child.on('close', (code) => {
  clearTimeout(timeout);
  assert(initialized && listed, 'MCP handshake did not complete');
  assert(code === 0, `MCP server exited with ${code}: ${stderr}`);
  assert(!stderr.includes('ask_smoke_fixture_do_not_use'), 'credential leaked to stderr');
});

send({
  jsonrpc: '2.0',
  id: 1,
  method: 'initialize',
  params: {
    protocolVersion: '2025-11-25',
    capabilities: {},
    clientInfo: { name: 'aispace-release-smoke', version: '1' },
  },
});

function assert(condition, message) {
  if (!condition) throw new Error(message);
}
