import { execFileSync, spawn } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, realpathSync, renameSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { gitLastUpdated, includeThemeFile, mergePublicAssets } from './build-utils.mjs';
import { collectSharedPages } from '../../runatlantis.io/.vitepress/shared-pages.mjs';

const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const args = process.argv.slice(2);
const concurrency = Number(process.env.ATLANTIS_DOCS_BUILD_CONCURRENCY || 1);
if (!Number.isInteger(concurrency) || concurrency < 1 || concurrency > 8) throw new Error('ATLANTIS_DOCS_BUILD_CONCURRENCY must be an integer from 1 to 8');
let requested;
let listVersions = false;
let output = path.join(tmpdir(), 'atlantis-versioned-docs');
for (let i = 0; i < args.length; i++) {
  if (args[i] === '--versions' && args[i + 1]) requested = args[++i].split(',');
  else if (args[i] === '--outDir' && args[i + 1]) output = path.resolve(args[++i]);
  else if (args[i] === '--list-versions') listVersions = true;
  else throw new Error(`unknown or incomplete option ${args[i]}`);
}
if (repo.startsWith(path.resolve(output) + path.sep) || path.resolve(output) === repo) throw new Error('output must be a dedicated build directory');
const outputMarker = path.join(output, '.atlantis-docs-build');
if (existsSync(output) && readdirSync(output).length && !existsSync(outputMarker) && !existsSync(path.join(output, 'doc-versions.json'))) {
  throw new Error('output directory is not an existing versioned documentation build; choose an empty directory');
}
const git = (...argv) => execFileSync('git', argv, { cwd: repo, maxBuffer: 128 * 1024 * 1024 });
const stable = git('tag', '--list', 'v*').toString().trim().split('\n').filter((tag) => /^v\d+\.\d+\.\d+$/.test(tag));
stable.sort((a, b) => b.localeCompare(a, undefined, { numeric: true }));
const objects = execFileSync('git', ['cat-file', '--batch-check=%(objecttype)'], {
  cwd: repo, input: stable.map((tag) => `${tag}:runatlantis.io/docs/server-configuration.md`).join('\n') + '\n',
}).toString().trim().split('\n');
const available = stable.filter((_, i) => objects[i] === 'blob');
if (listVersions) {
  console.log(available.join('\n'));
  process.exit(0);
}
const seen = new Set();
const tags = requested?.length === 1 && requested[0] === 'all' ? available : requested ?? available.filter((tag) => {
  const minor = tag.split('.').slice(0, 2).join('.');
  if (seen.has(minor)) return false;
  seen.add(minor);
  return true;
}).slice(0, 3);
if (!tags.length || new Set(tags).size !== tags.length) throw new Error('select at least one distinct release tag');
for (const tag of tags) {
  if (!/^v\d+\.\d+\.\d+$/.test(tag) || !available.includes(tag)) throw new Error(`release tag is not available locally with configuration documentation: ${tag}`);
}
tags.sort((a, b) => b.localeCompare(a, undefined, { numeric: true }));

const workspace = mkdtempSync(path.join(realpathSync(tmpdir()), 'atlantis-doc-build-'));
function pagesIn(directory, prefix = '') {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    if (entry.name.startsWith('.') || entry.name === 'node_modules') return [];
    const relative = path.posix.join(prefix, entry.name);
    return entry.isDirectory() ? pagesIn(path.join(directory, entry.name), relative) : relative.endsWith('.md') ? [relative] : [];
  });
}
function normalizeReadmes(directory) {
  if (!existsSync(directory)) return;
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const filename = path.join(directory, entry.name);
    if (entry.isDirectory()) normalizeReadmes(filename);
    else if (entry.name === 'README.md' && !existsSync(path.join(directory, 'index.md'))) renameSync(filename, path.join(directory, 'index.md'));
  }
}
const snapshots = [];
const folders = ['docs', 'guide', 'logs', 'hosting', 'terraform', 'public', 'es/docs', 'es/guide'];
const landingPages = ['docs.md', 'guide.md', 'es/docs.md', 'es/guide.md'];
try {
  for (const version of [tags[0], 'main', ...tags.slice(1)]) {
    let historical = path.join(repo, 'runatlantis.io');
    if (version !== 'main') {
      const archive = path.join(workspace, `${version}-source`);
      mkdirSync(archive);
      execFileSync('tar', ['-xf', '-', '-C', archive], { input: git('archive', '--format=tar', version, 'runatlantis.io') });
      historical = path.join(archive, 'runatlantis.io');
    }
    const site = path.join(workspace, version);
    mkdirSync(site);
    const theme = path.join(repo, 'runatlantis.io/.vitepress');
    cpSync(theme, path.join(site, '.vitepress'), { recursive: true, filter: (source) => includeThemeFile(theme, source) });
    for (const folder of folders) {
      if (existsSync(path.join(historical, folder))) cpSync(path.join(historical, folder), path.join(site, folder), { recursive: true });
    }
    for (const folder of ['.vuepress/public', '.vitepress/public']) {
      if (existsSync(path.join(historical, folder))) cpSync(path.join(historical, folder), path.join(site, 'public'), { recursive: true });
    }
    for (const file of landingPages) {
      if (existsSync(path.join(historical, file))) cpSync(path.join(historical, file), path.join(site, file));
    }
    if (!existsSync(path.join(site, 'public'))) cpSync(path.join(repo, 'runatlantis.io/public'), path.join(site, 'public'), { recursive: true });
    const originals = pagesIn(site);
    for (const folder of ['docs', 'guide', 'logs', 'hosting', 'es/docs', 'es/guide']) normalizeReadmes(path.join(site, folder));
    const sources = Object.fromEntries(originals.map((file) => [
      existsSync(path.join(site, file)) ? file : file.replace(/(^|\/)README\.md$/, '$1index.md'), file,
    ]));
    const configPages = [
      ['Server configuration', 'server-configuration'],
      ['Server-side repository configuration', 'server-side-repo-config'],
      ['Repository configuration', 'repo-level-atlantis-yaml'],
      ['Repository configuration', 'atlantis-yaml-reference'],
    ].filter(([, page]) => existsSync(path.join(site, `docs/${page}.md`)));
    const overview = `# Atlantis ${version} configuration\n\nRelease snapshot.\n\n${configPages.map(([label, page]) => `- [${label}](/docs/${page}.md)\n`).join('')}`;
    writeFileSync(path.join(site, 'index.md'), overview);
    if (!existsSync(path.join(site, 'docs.md')) && !existsSync(path.join(site, 'docs/index.md'))) writeFileSync(path.join(site, 'docs.md'), overview);
    if (existsSync(path.join(site, 'es/docs'))) {
      // Keep language-menu routes valid when a release has partial translations.
      for (const folder of ['docs', 'guide']) {
        if (!existsSync(path.join(site, folder))) continue;
        for (const page of pagesIn(path.join(site, folder))) {
          const translated = path.join(site, 'es', folder, page);
          if (existsSync(translated)) continue;
          mkdirSync(path.dirname(translated), { recursive: true });
          writeFileSync(translated, `::: info\nEsta página está disponible en inglés. [Leer el original](/${folder}/${page}).\n:::\n\n${readFileSync(path.join(site, folder, page), 'utf8')}`);
          const original = sources[`${folder}/${page}`];
          if (original) sources[`es/${folder}/${page}`] = original;
        }
      }
      const translatedOverview = `# Configuración de Atlantis ${version}\n\n${configPages.map(([label, page]) => `- [${label}](${existsSync(path.join(site, `es/docs/${page}.md`)) ? '/es/docs/' : '/docs/'}${page}.md)\n`).join('')}`;
      writeFileSync(path.join(site, 'es/index.md'), translatedOverview);
      if (!existsSync(path.join(site, 'es/docs.md')) && !existsSync(path.join(site, 'es/docs/index.md'))) writeFileSync(path.join(site, 'es/docs.md'), translatedOverview);
    }
    symlinkSync(path.join(repo, 'node_modules'), path.join(site, 'node_modules'), 'dir');
    writeFileSync(path.join(site, 'package.json'), '{"type":"module"}\n');
    snapshots.push({ version, site, pages: pagesIn(site), sources });
  }
  const manifest = snapshots.map(({ version, pages }) => ({ version, pages }));
  const manifestFile = path.join(workspace, 'doc-versions.json');
  writeFileSync(manifestFile, JSON.stringify(manifest));
  const metadataFile = path.join(workspace, 'build-metadata.json');
  writeFileSync(metadataFile, JSON.stringify({
    sharedPages: collectSharedPages(path.join(repo, 'runatlantis.io')),
    sources: Object.fromEntries(snapshots.map(({ version, sources }) => [version, sources])),
    lastUpdated: { main: gitLastUpdated(repo, 'HEAD'), [tags[0]]: gitLastUpdated(repo, tags[0]) },
  }));
  const rootSite = path.join(workspace, 'current');
  const latest = snapshots[0];
  cpSync(path.join(repo, 'runatlantis.io'), rootSite, { recursive: true, filter: (source) => {
    const relative = path.relative(path.join(repo, 'runatlantis.io'), source).split(path.sep).join('/');
    return !/^(?:\.vitepress\/(?:dist|cache)|node_modules|e2e)(\/|$)/.test(relative)
      && !/^(?:(?:es\/)?(?:docs|guide)(?:\/|\.md$)|logs\/|hosting\/|terraform\/)/.test(relative);
  } });
  for (const folder of folders) {
    if (!existsSync(path.join(latest.site, folder))) continue;
    if (folder === 'public') mergePublicAssets(path.join(latest.site, folder), path.join(rootSite, folder));
    else cpSync(path.join(latest.site, folder), path.join(rootSite, folder), { recursive: true });
  }
  for (const file of landingPages) {
    if (existsSync(path.join(latest.site, file))) cpSync(path.join(latest.site, file), path.join(rootSite, file));
  }
  mkdirSync(path.join(workspace, 'docs'), { recursive: true });
  symlinkSync(path.join(repo, 'docs/adr'), path.join(workspace, 'docs/adr'), 'dir');
  symlinkSync(path.join(repo, 'node_modules'), path.join(rootSite, 'node_modules'), 'dir');
  writeFileSync(path.join(rootSite, 'package.json'), '{"type":"module"}\n');
  async function build({ version, site }) {
    const destination = version ? path.join(output, 'versions', version) : output;
    console.log(`Building ${version || 'main'} → ${destination}`);
    await new Promise((resolve, reject) => {
      const child = spawn(process.execPath, [path.join(repo, 'node_modules/vitepress/bin/vitepress.js'), 'build', site, '--outDir', destination], {
        cwd: repo, stdio: 'inherit', env: { ...process.env, ATLANTIS_DOC_VERSION: version, ATLANTIS_DOC_VERSIONS_FILE: manifestFile, ATLANTIS_DOC_BUILD_METADATA_FILE: metadataFile },
      });
      child.on('error', reject);
      child.on('close', (code) => code === 0 ? resolve() : reject(new Error(`building ${version || 'main website'} failed`)));
    });
  }
  await build({ version: '', site: rootSite });
  let next = 0;
  let failure;
  await Promise.all(Array.from({ length: Math.min(concurrency, snapshots.length) }, async () => {
    while (!failure && next < snapshots.length) {
      try { await build(snapshots[next++]); }
      catch (error) { failure = error; }
    }
  }));
  if (failure) throw failure;
  writeFileSync(path.join(output, 'doc-versions.json'), JSON.stringify(manifest, null, 2) + '\n');
  console.log(`Versioned documentation built at ${output}`);
} finally {
  rmSync(workspace, { recursive: true, force: true });
  if (existsSync(output)) writeFileSync(outputMarker, 'Generated documentation output. Rebuildable from Git tags.\n');
}
