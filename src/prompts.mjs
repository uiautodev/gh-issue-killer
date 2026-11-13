import fs from 'node:fs';
import path from 'node:path';

const DIR = path.join(import.meta.dirname, '..', 'prompts');
const cache = new Map();

export function renderPrompt(name, variables) {
  if (!cache.has(name)) {
    cache.set(name, fs.readFileSync(path.join(DIR, `${name}.md`), 'utf8'));
  }
  let text = cache.get(name);
  for (const [key, value] of Object.entries(variables)) {
    text = text.split(`{{${key}}}`).join(value ?? '');
  }
  return text;
}
