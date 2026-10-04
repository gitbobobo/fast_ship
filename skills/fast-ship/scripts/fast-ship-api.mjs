// Fast Ship REST API helper for agents. Reads base_url and api_key from
// ~/.config/fast-ship/config.yaml and attaches the Authorization header
// itself, so the API key never appears on the command line, in env vars,
// or in output. Prints only the response body to stdout.
//
// Usage: node fast-ship-api.mjs <METHOD> <path> [body.json] [--verify]

import { readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { argv, stderr, stdout } from 'node:process';

const CONFIG_PATH = join(homedir(), '.config', 'fast-ship', 'config.yaml');
const TIMEOUT_MS = 30_000;

const USAGE = `Usage: node fast-ship-api.mjs <METHOD> <path> [body.json] [--verify]

  METHOD      HTTP method, e.g. GET / POST / PUT / DELETE
  path        API path appended to base_url; may include a query string
  body.json   UTF-8 JSON file sent as the request body (write requests)
  --verify    after a 2xx response, GET the same path and print the
              read-back result as { "response": ..., "verify": ... }

Credentials come from ~/.config/fast-ship/config.yaml (base_url, api_key).`;

class CliError extends Error {}

function fail(message) {
  throw new CliError(message);
}

// Removes values that must not appear in output: the API key and any URLs
// derived from config (Node's fetch errors embed the full request URL).
function redact(message, secrets) {
  let out = message;
  for (const s of secrets) {
    if (s) out = out.split(s).join('<redacted>');
  }
  return out;
}

// Extracts the top-level base_url / api_key values: handles unquoted,
// single- or double-quoted values, inline comments, and blank space.
function parseConfig(text) {
  const out = {};
  for (const line of text.replace(/^\uFEFF/, '').split(/\r?\n/)) {
    const m = line.match(/^(base_url|api_key)\s*:\s*(.*)$/);
    if (!m || m[1] in out) continue;
    const rest = m[2].trim();
    if (rest.startsWith('"') || rest.startsWith("'")) {
      const end = rest.indexOf(rest[0], 1);
      out[m[1]] = end === -1 ? rest.slice(1) : rest.slice(1, end);
    } else {
      const hash = rest.search(/(?:^|\s)#/);
      out[m[1]] = (hash === -1 ? rest : rest.slice(0, hash)).trim();
    }
  }
  return out;
}

function loadConfig() {
  let text;
  try {
    text = readFileSync(CONFIG_PATH, 'utf8');
  } catch {
    fail(`cannot read ${CONFIG_PATH}; create it with base_url and api_key`);
  }
  const { base_url: baseUrl, api_key: apiKey } = parseConfig(text);
  if (!baseUrl || !apiKey) {
    fail(`${CONFIG_PATH} must define base_url and api_key`);
  }
  return { baseUrl, apiKey };
}

async function request(method, url, apiKey, body) {
  try {
    return await fetch(url, {
      method,
      headers: {
        Authorization: `Bearer ${apiKey}`,
        'Content-Type': 'application/json; charset=utf-8',
      },
      body,
      signal: AbortSignal.timeout(TIMEOUT_MS),
    });
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    fail(`request failed: ${redact(msg, [apiKey, url])}`);
  }
}

function parseJson(text) {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

async function main() {
  const args = argv.slice(2);
  if (args.length === 0) {
    stderr.write(`${USAGE}\n`);
    process.exitCode = 1;
    return;
  }
  if (args.includes('-h') || args.includes('--help')) {
    stdout.write(`${USAGE}\n`);
    return;
  }

  const positional = [];
  let verify = false;
  for (const arg of args) {
    if (arg === '--verify') verify = true;
    else positional.push(arg);
  }
  const [method, path, bodyFile] = positional;
  if (!method || !path) {
    stderr.write(`${USAGE}\n`);
    process.exitCode = 1;
    return;
  }

  let body;
  if (bodyFile) {
    try {
      body = readFileSync(bodyFile);
    } catch {
      fail(`cannot read body file: ${bodyFile}`);
    }
  }

  const { baseUrl, apiKey } = loadConfig();
  const url = `${baseUrl.replace(/\/+$/, '')}/${path.replace(/^\/+/, '')}`;

  const res = await request(method.toUpperCase(), url, apiKey, body);
  const text = await res.text();
  if (!res.ok) {
    stdout.write(`${text}\n`);
    fail(`request returned status ${res.status}`);
  }

  if (!verify) {
    stdout.write(`${text}\n`);
    return;
  }

  const verifyRes = await request('GET', url, apiKey, undefined);
  const verifyText = await verifyRes.text();
  stdout.write(`${JSON.stringify({ response: parseJson(text), verify: parseJson(verifyText) }, null, 2)}\n`);
  if (!verifyRes.ok) {
    fail(`verify GET returned status ${verifyRes.status}`);
  }
}

main().catch((err) => {
  stderr.write(`fast-ship-api: ${err instanceof Error ? err.message : String(err)}\n`);
  process.exitCode = 1;
});
