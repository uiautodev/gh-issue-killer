import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { spawn } from 'node:child_process';

export const HOME = os.homedir();
export const STATE_DIR = process.env.GWIK_STATE_DIR || path.join(HOME, '.gh-issue-killer');
export const LOG_DIR = path.join(STATE_DIR, 'logs');
export const RUN_DIR = path.join(STATE_DIR, 'runs');
export const TMP_DIR = path.join(STATE_DIR, 'tmp');
export const BIN_DIR = path.join(STATE_DIR, 'bin');

export function expandHome(value) {
  if (!value) return value;
  if (value === '~') return HOME;
  if (value.startsWith('~/')) return path.join(HOME, value.slice(2));
  return value;
}

export function ensureDir(dir) {
  fs.mkdirSync(dir, { recursive: true });
  return dir;
}

export function ensureDirs() {
  for (const dir of [STATE_DIR, LOG_DIR, RUN_DIR, TMP_DIR, BIN_DIR]) ensureDir(dir);
}

export function nowIso() {
  return new Date().toISOString();
}

export function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export function readJsonSync(file, fallback = null) {
  try {
    return JSON.parse(fs.readFileSync(file, 'utf8'));
  } catch {
    return fallback;
  }
}

export function atomicWriteFile(file, content) {
  ensureDir(path.dirname(file));
  const tmp = `${file}.tmp-${process.pid}-${Date.now()}`;
  fs.writeFileSync(tmp, content);
  fs.renameSync(tmp, file);
}

export function atomicWriteJson(file, value) {
  atomicWriteFile(file, JSON.stringify(value, null, 2) + '\n');
}

export function randomHex(bytes = 16) {
  return Buffer.from(Array.from({ length: bytes }, () => Math.floor(Math.random() * 256))).toString(
    'hex'
  );
}

const LEVELS = { debug: 10, info: 20, warn: 30, error: 40 };

export function createLogger(name = 'daemon') {
  const level = LEVELS[process.env.GWIK_LOG_LEVEL || 'info'] ?? LEVELS.info;
  const logFile = path.join(LOG_DIR, `${name}.log`);
  ensureDir(LOG_DIR);
  const stream = fs.createWriteStream(logFile, { flags: 'a' });

  function write(kind, message, extra) {
    if (LEVELS[kind] < level) return;
    const ts = nowIso();
    let line = `${ts} ${kind.toUpperCase().padEnd(5)} ${message}`;
    if (extra !== undefined) {
      try {
        line += ` ${typeof extra === 'string' ? extra : JSON.stringify(extra)}`;
      } catch {
        line += ' [unserializable]';
      }
    }
    console.log(line);
    stream.write(line + '\n');
  }

  return {
    debug: (message, extra) => write('debug', message, extra),
    info: (message, extra) => write('info', message, extra),
    warn: (message, extra) => write('warn', message, extra),
    error: (message, extra) => write('error', message, extra),
    file: logFile
  };
}

export function whichSync(name) {
  const dirs = (process.env.PATH || '').split(path.delimiter);
  for (const dir of dirs) {
    if (!dir) continue;
    const candidate = path.join(dir, name);
    try {
      fs.accessSync(candidate, fs.constants.X_OK);
      return candidate;
    } catch {
      // keep looking
    }
  }
  return null;
}

function collect(stream, maxBytes = 2 * 1024 * 1024) {
  let data = '';
  stream.on('data', (chunk) => {
    data += chunk.toString();
    if (data.length > maxBytes) data = data.slice(-maxBytes);
  });
  return () => data;
}

export function run(command, args = [], options = {}) {
  const {
    cwd,
    env = process.env,
    timeout = 120_000,
    allowFailure = false,
    input = null
  } = options;

  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd,
      env,
      stdio: [input === null ? 'ignore' : 'pipe', 'pipe', 'pipe']
    });
    const getOut = collect(child.stdout);
    const getErr = collect(child.stderr);
    let timer = null;
    let timedOut = false;

    if (input !== null) {
      child.stdin.end(input);
    }

    if (timeout > 0) {
      timer = setTimeout(() => {
        timedOut = true;
        child.kill('SIGTERM');
        setTimeout(() => child.kill('SIGKILL'), 5_000).unref();
      }, timeout);
    }

    child.on('error', (error) => {
      if (timer) clearTimeout(timer);
      reject(error);
    });

    child.on('close', (code, signal) => {
      if (timer) clearTimeout(timer);
      const result = {
        code: code ?? (timedOut ? 124 : 1),
        signal,
        timedOut,
        stdout: getOut(),
        stderr: getErr()
      };
      if (result.code !== 0 && !allowFailure) {
        const error = new Error(
          `${command} ${args.join(' ')} failed with code ${result.code}: ${result.stderr.slice(-2000)}`
        );
        error.result = result;
        reject(error);
        return;
      }
      resolve(result);
    });
  });
}

export function runShell(command, options = {}) {
  return run('/bin/sh', ['-c', command], options);
}

export function tail(text, lines = 40) {
  if (!text) return '';
  const parts = text.trimEnd().split('\n');
  return parts.slice(Math.max(0, parts.length - lines)).join('\n');
}

export function shortId(value, length = 6) {
  const text = String(value ?? '');
  if (!text) return randomHex(3);
  let hash = 0;
  for (let i = 0; i < text.length; i += 1) {
    hash = (hash * 31 + text.charCodeAt(i)) >>> 0;
  }
  return hash.toString(36).padStart(length, '0').slice(-length);
}
