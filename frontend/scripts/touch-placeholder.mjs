// adapter-static owns the pages directory and deletes unknown files, so the
// tracked .gitkeep has to come back after every build. Without it a checkout
// that never ran npm would not satisfy `//go:embed all:web/dist`.
import { writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const placeholder = fileURLToPath(
	new URL('../../cmd/issuekiller/web/dist/.gitkeep', import.meta.url)
);

writeFileSync(placeholder, '');
