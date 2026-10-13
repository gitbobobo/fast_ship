// Fast Ship REST API helper for agents. Reads base_url and api_key from
// ~/.config/fast-ship/config.yaml and attaches the Authorization header
// itself, so the API key never appears on the command line, in env vars,
// or in output. Prints only the response body to stdout.
//
// On failure the stderr line carries the envelope's business code and
// message (`request returned status 502: 50200 GitHub API 调用失败
// (github-upstream-error, retryable)`), so the reason survives callers
// piping stdout into tail/jq. Transport failures and 5xx get one
// automatic retry, but only where resending is provably safe (idempotent
// methods and the documented-idempotent pull-requests POSTs); a response
// whose body cannot be read is reported with its status, never resent.
//
// Usage: node fast-ship-api.mjs <METHOD> <path> [body.json] [--verify [get-path]]

import { readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { argv, stderr, stdout } from 'node:process';

const CONFIG_PATH = join(homedir(), '.config', 'fast-ship', 'config.yaml');
const TIMEOUT_MS = 30_000;
const RETRY_DELAY_MS = 1_000;

// Stable tags for the business codes agents hit most (server/internal/pkg/
// errs/errors.go), so a truncated pipeline still shows what failed and
// whether another attempt can help. Unknown codes print message only.
const CODE_HINTS = {
  40001: 'invalid-params',
  40003: 'project-github-not-configured',
  40004: 'image-too-large',
  40102: 'invalid-api-key',
  40301: 'jwt-only-operation',
  40303: 'api-key-only-operation',
  40401: 'project-not-found',
  40405: 'issue-not-found',
  40409: 'log-run-not-found',
  40412: 'pr-link-not-found',
  40416: 'screenshot-annotation-not-found',
  40907: 'issue-sync-running',
  40908: 'issue-read-only',
  40909: 'log-run-entry-limit',
  40910: 'issue-not-recommendable',
  40911: 'recommendation-deferred',
  41201: 'too-many-issues-matched',
  50000: 'internal-error',
  50200: 'github-upstream-error, retryable',
  50201: 'ai-upstream-error, retryable',
};

const USAGE = `Usage: node fast-ship-api.mjs <METHOD> <path> [body.json] [--verify]

  METHOD      HTTP method, e.g. GET / POST / PUT / DELETE
  path        API path appended to base_url; may include a query string
  body.json   UTF-8 JSON file sent as the request body (write requests)
  --verify    after a 2xx response, issue a GET and print the read-back
              as { "response": ..., "verify": ... }; GETs the same path
              unless get-path is given (use it for write-only endpoints
              that have no GET route); after DELETE a 404 confirms removal

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

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// Only idempotent calls may be resent: GET/PUT/DELETE are upserts or
// removals here, and POST is allowed solely on the pull-requests routes
// the API documents as idempotent (attach dedupes by issue+PR, sync only
// refreshes rows). Other POSTs create rows; a lost/5xx response does not
// mean the server didn't apply them, so they are never auto-retried.
function retryable(method, path) {
  if (method !== 'POST') return true;
  return /\/pull-requests(?:\/sync)?$/.test(path.split('?')[0]);
}

// Resolves to { status, ok, text, readError? }. Transient failures get
// exactly one retry after a short delay — a transport error may mean the
// request never left, and resending after a 5xx is safe only because
// retryable() limits retries to idempotent requests. A response whose
// body fails to read is reported with its status instead of resent: the
// request may already have been applied server-side. When the retry
// itself dies at transport level, the earlier 5xx response is still
// reported so the caller sees the server's envelope.
async function request(method, url, apiKey, body, path) {
  const attempts = retryable(method, path) ? 2 : 1;
  let last5xx;
  let lastErr;
  for (let attempt = 0; attempt < attempts; attempt++) {
    if (attempt > 0) await sleep(RETRY_DELAY_MS);
    let res;
    try {
      res = await fetch(url, {
        method,
        headers: {
          Authorization: `Bearer ${apiKey}`,
          'Content-Type': 'application/json; charset=utf-8',
        },
        body,
        signal: AbortSignal.timeout(TIMEOUT_MS),
      });
    } catch (err) {
      lastErr = err;
      continue;
    }
    let text;
    try {
      text = await res.text();
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      return { status: res.status, ok: res.ok, text: '', readError: msg };
    }
    if (res.status < 500 || attempt === attempts - 1) {
      return { status: res.status, ok: res.ok, text };
    }
    last5xx = { status: res.status, ok: res.ok, text };
  }
  if (last5xx) return last5xx;
  const msg = lastErr instanceof Error ? lastErr.message : String(lastErr);
  fail(`request failed: ${redact(msg, [apiKey, url])}`);
}

function parseJson(text) {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

// Builds the single stderr line for a failed response: HTTP status plus
// the envelope's business code/message and a hint tag when the code is a
// known one. Non-envelope bodies (bare 413, proxy error pages) degrade to
// a squashed snippet so the line is never just a bare status.
function describeError(status, text) {
  const env = parseJson(text);
  if (
    env &&
    typeof env === 'object' &&
    typeof env.code === 'number' &&
    env.code !== 0 &&
    typeof env.message === 'string' &&
    env.message
  ) {
    const hint = CODE_HINTS[env.code];
    return `returned status ${status}: ${env.code} ${env.message}${hint ? ` (${hint})` : ''}`;
  }
  const snippet = String(text).replace(/\s+/g, ' ').trim().slice(0, 200);
  return `returned status ${status}${snippet ? `: ${snippet}` : ''}`;
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
  let verifyPath;
  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (arg === '--verify') {
      verify = true;
      const next = args[i + 1];
      if (next && !next.startsWith('-')) {
        verifyPath = next;
        i++;
      }
    } else {
      positional.push(arg);
    }
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

  const res = await request(method.toUpperCase(), url, apiKey, body, path);
  const text = res.text;
  if (res.readError) {
    fail(`request returned status ${res.status} but the response body could not be read: ${res.readError}`);
  }
  if (!res.ok) {
    stdout.write(`${text}\n`);
    fail(`request ${describeError(res.status, text)}`);
  }

  if (!verify) {
    stdout.write(`${text}\n`);
    return;
  }

  const verifyUrl = `${baseUrl.replace(/\/+$/, '')}/${(verifyPath ?? path).replace(/^\/+/, '')}`;
  const verifyRes = await request('GET', verifyUrl, apiKey, undefined, verifyPath ?? path);
  const verifyText = verifyRes.text;
  if (verifyRes.readError) {
    fail(`verify GET returned status ${verifyRes.status} but the response body could not be read: ${verifyRes.readError}`);
  }
  stdout.write(`${JSON.stringify({ response: parseJson(text), verify: parseJson(verifyText) }, null, 2)}\n`);
  if (method.toUpperCase() === 'DELETE') {
    // After a delete, a 404 read-back is the expected confirmation.
    if (verifyRes.ok) {
      fail('verify GET still returns the resource after DELETE');
    }
    if (verifyRes.status !== 404) {
      fail(`verify GET ${describeError(verifyRes.status, verifyText)}`);
    }
    return;
  }
  if (!verifyRes.ok) {
    fail(`verify GET ${describeError(verifyRes.status, verifyText)}`);
  }
}

main().catch((err) => {
  stderr.write(`fast-ship-api: ${err instanceof Error ? err.message : String(err)}\n`);
  process.exitCode = 1;
});
