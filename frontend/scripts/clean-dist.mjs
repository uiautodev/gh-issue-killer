// adapter-static writes into the Go embed directory but leaves files from the
// previous build behind. Remove everything except the tracked placeholder so a
// rebuilt app never embeds stale hashed assets.
import { readdirSync, rmSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';

const dist = fileURLToPath(new URL('../../cmd/gh-issue-killer/web/dist', import.meta.url));

for (const entry of readdirSync(dist)) {
	if (entry === '.gitkeep') continue;
	rmSync(join(dist, entry), { recursive: true, force: true });
}
