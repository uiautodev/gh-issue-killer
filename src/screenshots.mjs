import fs from 'node:fs';
import path from 'node:path';
import { run, ensureDir } from './util.mjs';
import { curlProxyArgs } from './proxy.mjs';

const MIME = {
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.mp4': 'video/mp4'
};

async function probe(file) {
  const result = await run(
    'ffprobe',
    ['-v', 'error', '-select_streams', 'v:0', '-show_entries', 'stream=width,height', '-of', 'csv=p=0', file],
    { allowFailure: true, timeout: 30_000 }
  );
  if (result.code !== 0) return null;
  const [width, height] = result.stdout.trim().split(',').map(Number);
  if (!width || !height) return null;
  return { width, height };
}

/**
 * Compose a before/after pair into a single side-by-side PNG (12px white gap,
 * both images padded to the same height). The user asked for one screenshot
 * where the left half is "before" and the right half is "after".
 */
export async function composeBeforeAfter(beforeFile, afterFile, outFile) {
  ensureDir(path.dirname(outFile));
  const before = await probe(beforeFile);
  const after = await probe(afterFile);
  if (!before || !after) throw new Error('cannot read before/after image dimensions');

  const maxHeight = Math.max(before.height, after.height);
  const filter = [
    `[0:v]pad=iw+12:${maxHeight}:0:(oh-ih)/2:color=white[a]`,
    `[1:v]pad=iw:${maxHeight}:0:(oh-ih)/2:color=white[b]`,
    '[a][b]hstack=inputs=2[out]'
  ].join(';');

  const result = await run(
    'ffmpeg',
    ['-y', '-loglevel', 'error', '-i', beforeFile, '-i', afterFile, '-filter_complex', filter, '-map', '[out]', outFile],
    { allowFailure: true, timeout: 120_000 }
  );
  if (result.code !== 0) {
    throw new Error(`ffmpeg compose failed: ${result.stderr.slice(-800)}`);
  }
  return outFile;
}

async function upload({ file, repoId, token, proxy }) {
  const name = path.basename(file);
  const mime = MIME[path.extname(file).toLowerCase()] || 'application/octet-stream';
  const endpoint =
    `https://uploads.github.com/user-attachments/assets` +
    `?name=${encodeURIComponent(name)}&content_type=${encodeURIComponent(mime)}&repository_id=${repoId}`;

  const attempts = [[...curlProxyArgs(proxy)], []];
  let lastError = null;
  for (const proxyArgs of attempts) {
    try {
      const result = await run(
        'curl',
        [
          '-sS',
          '--max-time',
          '180',
          ...proxyArgs,
          '-X',
          'POST',
          endpoint,
          '-H',
          `Authorization: Bearer ${token}`,
          '-H',
          'Accept: application/json',
          '--data-binary',
          `@${file}`
        ],
        { timeout: 200_000, allowFailure: true }
      );
      if (result.code !== 0) {
        lastError = result.stderr || `curl exited ${result.code}`;
        continue;
      }
      const parsed = JSON.parse(result.stdout);
      if (parsed.url) return parsed.url;
      lastError = result.stdout.slice(0, 500);
    } catch (error) {
      lastError = error.message;
    }
  }
  throw new Error(`screenshot upload failed for ${name}: ${lastError}`);
}

function resolveFile(runDir, value) {
  if (!value) return null;
  const base = path.resolve(runDir);
  const file = path.isAbsolute(value) ? value : path.resolve(base, value);
  if (!file.startsWith(base + path.sep) && file !== base) return null;
  return fs.existsSync(file) ? file : null;
}

/**
 * result.json.screenshots supports:
 *   ["screenshots/after.png"]                        -> upload as-is
 *   [{ "caption": "...", "before": "...", "after": "..." }] -> compose + upload one image
 *   [{ "caption": "...", "combined": "..." }]        -> upload the combined image
 *   [{ "caption": "...", "after": "..." }]           -> upload after only (missing before warning)
 */
export async function collectScreenshots({ runDir, manifest, repoId, token, proxy, log }) {
  const entries = Array.isArray(manifest?.screenshots) ? manifest.screenshots : [];
  if (entries.length === 0) return [];

  const shots = [];
  let index = 0;

  for (const entry of entries) {
    index += 1;
    const item = typeof entry === 'string' ? { after: entry } : entry || {};
    const caption = item.caption || `UI change ${index}`;

    try {
      let file = null;

      if (item.combined) {
        file = resolveFile(runDir, item.combined);
      } else if (item.before && item.after) {
        const before = resolveFile(runDir, item.before);
        const after = resolveFile(runDir, item.after);
        if (!before || !after) {
          log?.warn(`screenshot pair for "${caption}" is missing before or after file`);
          continue;
        }
        file = path.join(runDir, 'screenshots', `combined-${String(index).padStart(2, '0')}.png`);
        await composeBeforeAfter(before, after, file);
        log?.info(`composed before/after: ${file}`);
      } else if (item.after) {
        file = resolveFile(runDir, item.after);
        log?.warn(`screenshot "${caption}" has no before image, uploading after only`);
      }

      if (!file || !fs.existsSync(file)) {
        log?.warn(`screenshot "${caption}" not found`);
        continue;
      }
      const stat = fs.statSync(file);
      if (stat.size === 0) continue;
      const url = await upload({ file, repoId, token, proxy });
      shots.push({ name: path.basename(file), caption, url, size: stat.size });
      log?.info(`screenshot uploaded: ${path.basename(file)} -> ${url}`);
    } catch (error) {
      log?.warn(error.message);
    }
  }

  return shots;
}

export function screenshotsMarkdown(shots) {
  if (!shots || shots.length === 0) return null;
  return shots
    .map((shot) => `**${shot.caption}**（左：改动前 / 右：改动后）\n\n![${shot.caption}](${shot.url})`)
    .join('\n\n');
}
