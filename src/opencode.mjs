import fs from 'node:fs';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { whichSync, ensureDir, tail, BIN_DIR } from './util.mjs';

const SHIM_DIR = BIN_DIR;
let SHIM_PREPARED = false;

export function prepareShim({ ghAgentBin }) {
  if (SHIM_PREPARED) return SHIM_DIR;
  ensureDir(SHIM_DIR);
  const gh = path.join(SHIM_DIR, 'gh');
  const script = [
    '#!/bin/sh',
    '# gh-issue-killer shim: force any `gh` call from child agents onto the gh-agent account.',
    `PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" exec "${ghAgentBin}" "$@"`,
    ''
  ].join('\n');
  fs.writeFileSync(gh, script);
  fs.chmodSync(gh, 0o755);
  SHIM_PREPARED = true;
  return SHIM_DIR;
}

export function childEnv({ baseEnv, shimDir }) {
  const env = { ...baseEnv };
  if (shimDir) env.PATH = `${shimDir}:${env.PATH || ''}`;
  return env;
}

export function opencodeBin() {
  return process.env.OPENCODE_BIN || whichSync('opencode');
}

export async function runOpencode({
  cwd,
  prompt,
  sessionId = null,
  agent = null,
  model = null,
  env = process.env,
  timeoutMs = 15 * 60 * 1000,
  logFile = null,
  log
}) {
  const bin = opencodeBin();
  if (!bin) throw new Error('opencode not found in PATH');

  const args = ['run'];
  if (sessionId) args.push('--session', sessionId);
  if (model) args.push('--model', model);
  if (agent && agent !== 'opencode') args.push('--agent', agent);
  args.push('--format', 'json', '--auto');

  const fullPrompt = prompt;
  log?.debug?.(`opencode spawn cwd=${cwd} session=${sessionId || 'new'} model=${model}`);

  return new Promise((resolve, reject) => {
    const child = spawn(bin, [...args, fullPrompt], {
      cwd,
      env,
      stdio: ['ignore', 'pipe', 'pipe']
    });

    let stdoutBuf = '';
    let stderrBuf = '';
    let outSession = sessionId || null;
    const texts = [];
    let sawError = null;
    let timer = null;
    let timedOut = false;

    const appendLog = (line) => {
      if (!logFile) return;
      try {
        fs.appendFileSync(logFile, line + '\n');
      } catch {
        // ignore
      }
    };

    child.stdout.on('data', (chunk) => {
      stdoutBuf += chunk.toString();
      const lines = stdoutBuf.split('\n');
      stdoutBuf = lines.pop() || '';
      for (const line of lines) {
        const trimmed = line.trim();
        if (!trimmed) continue;
        appendLog(trimmed);
        let event = null;
        try {
          event = JSON.parse(trimmed);
        } catch {
          continue;
        }
        if (event.sessionID) outSession = event.sessionID;
        if (event.type === 'text' && event.part?.text) texts.push(event.part.text);
        if (event.type === 'error') {
          sawError = event.error?.message || JSON.stringify(event.error);
        }
      }
    });

    child.stderr.on('data', (chunk) => {
      stderrBuf += chunk.toString();
      appendLog(chunk.toString().trimEnd());
    });

    if (timeoutMs > 0) {
      timer = setTimeout(() => {
        timedOut = true;
        child.kill('SIGTERM');
        setTimeout(() => child.kill('SIGKILL'), 10_000).unref();
      }, timeoutMs);
    }

    child.on('error', (error) => {
      if (timer) clearTimeout(timer);
      reject(error);
    });

    child.on('close', (code, signal) => {
      if (timer) clearTimeout(timer);
      const text = texts.join('').trim();
      const result = {
        sessionId: outSession,
        text,
        code: code ?? (timedOut ? 124 : 1),
        signal,
        timedOut,
        stderr: tail(stderrBuf, 50),
        error: sawError
      };
      if (result.code !== 0) {
        const error = new Error(
          `opencode exited with ${result.code}${timedOut ? ' (timeout)' : ''}: ${result.stderr || sawError || 'no output'}`
        );
        error.result = result;
        reject(error);
        return;
      }
      if (!text && sawError) {
        reject(new Error(`opencode failed: ${sawError}`));
        return;
      }
      resolve(result);
    });
  });
}
