import { execFileSync } from 'node:child_process';
import { cpSync } from 'node:fs';
import path from 'node:path';

export function includeThemeFile(root, source) {
  const relative = path.relative(root, source).split(path.sep).join('/');
  return !/^(dist|cache)(\/|$)/.test(relative) && !/config\.ts\.timestamp-/.test(relative);
}

export function gitLastUpdated(repo, ref) {
  const files = execFileSync('git', ['ls-tree', '-r', '-z', '--name-only', ref, '--', 'runatlantis.io'], {
    cwd: repo, encoding: 'utf8', maxBuffer: 128 * 1024 * 1024,
  }).split('\0').filter((file) => file.endsWith('.md'));
  const dates = {};
  // Per-file history simplification must match VitePress, including merges.
  for (const file of files) {
    const authorDate = execFileSync('git', ['log', '-1', '--format=%at', ref, '--', file], { cwd: repo, encoding: 'utf8' }).trim();
    if (authorDate) dates[file.slice('runatlantis.io/'.length)] = Number(authorDate) * 1000;
  }
  return dates;
}

export function fetchDocumentationHistory(repo) {
  const shallow = execFileSync('git', ['rev-parse', '--is-shallow-repository'], { cwd: repo, encoding: 'utf8' }).trim() === 'true';
  execFileSync('git', ['fetch', '--tags', ...(shallow ? ['--unshallow'] : []), 'origin'], { cwd: repo, stdio: 'inherit' });
}

export function mergePublicAssets(release, current) {
  cpSync(release, current, { recursive: true, force: false });
}
