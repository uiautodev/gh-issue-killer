import fs from 'node:fs';
import path from 'node:path';
import { run } from './util.mjs';
import { curlProxyArgs } from './proxy.mjs';

const MIME = {
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.mp4': 'video/mp4'
};

async function upload({ file, repoId, token, proxy }) {
  const name = path.basename(file);
  const mime = MIME[path.extname(file).toLowerCase()] || 'application/octet-stream';
  const endpoint =
    `https://uploads.github.com/user-attachments/assets` +
    `?name=${encodeURIComponent(name)}&content_type=${encodeURIComponent(mime)}&repository_id=${repoId}`;

  const attempts = [
    [...curlProxyArgs(proxy)],
    [] // retry without proxy
  ];

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

export async function collectScreenshots({ runDir, repoId, token, proxy, log }) {
  const dir = path.join(runDir, 'screenshots');
  if (!fs.existsSync(dir)) return [];
  const files = fs
    .readdirSync(dir)
    .filter((name) => /\.(png|jpe?g|gif|webp|mp4)$/i.test(name))
    .sort();

  const shots = [];
  for (const name of files) {
    const file = path.join(dir, name);
    const stat = fs.statSync(file);
    if (stat.size === 0) continue;
    try {
      const url = await upload({ file, repoId, token, proxy });
      shots.push({ name, url, size: stat.size });
      log.info(`screenshot uploaded: ${name} -> ${url}`);
    } catch (error) {
      log.warn(error.message);
    }
  }
  return shots;
}

export function screenshotsMarkdown(shots) {
  if (!shots || shots.length === 0) return null;
  return shots
    .map((shot) => {
      const caption = shot.name.replace(/\.(png|jpe?g|gif|webp|mp4)$/i, '').replace(/[-_]+/g, ' ');
      return `**${caption}**\n\n![${caption}](${shot.url})`;
    })
    .join('\n\n');
}
