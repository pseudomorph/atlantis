const siteHosts = new Set(['runatlantis.io', 'www.runatlantis.io']);
const sharedSection = /^\/(?:es\/)?(contributing|blog)(\/|\.|$)/;

export const versionBase = (version) => version ? `/versions/${version}/` : '/';

export function flagVersionBadges(md) {
  md.core.ruler.before('inline', 'flag-version-badges', (state) => {
    for (let i = 1; i < state.tokens.length; i++) {
      const token = state.tokens[i];
      if (state.tokens[i - 1].type === 'heading_open' && token.type === 'inline' && /^`--[^`]+`/.test(token.content)) {
        token.content = token.content.replace(/\s*<Badge\b(?=[^>]*\btext=(["'])v\d+\.\d+(?:\.\d+)?\+?\1)[^>]*\/>/gi, '');
      }
    }
  });
}

// Markdown links still pass through VitePress's base handling; raw HTML does not.
export function historicalLink(href, version, rawHTML = false) {
  if (!href || href.startsWith('#')) return href;
  let result = href;
  if (/^(https?:)?\/\//i.test(result)) {
    const url = new URL(result, 'https://www.runatlantis.io');
    if (!siteHosts.has(url.hostname) || !/^\/(?:es\/)?(docs|guide)(\/|\.|$)/.test(url.pathname)) return href;
    result = url.pathname + url.search + url.hash;
  }
  if (sharedSection.test(result)) return result.replace(/\.md(?=[?#]|$)/, '.html');
  result = result.replace(/(^|\/)README\.(md|html)(?=[?#]|$)/, '$1index.$2');
  result = result.replace(/(api-endpoints\.(?:md|html))#post-apidriftdetect$/, '$1#post-api-drift-detect');
  if (rawHTML && result.startsWith('/') && !result.startsWith('//') && !result.startsWith(versionBase(version))) {
    result = versionBase(version) + result.slice(1);
  }
  return result;
}

export function versionedLinks(md, version, versions = [], sharedPages) {
  md.core.ruler.after('inline', 'historical-links', (state) => {
    const relativePath = state.env.relativePath ?? '';
    const historical = Boolean(version) || /^(?:es\/)?(?:docs|guide)(\/|\.md$)/.test(relativePath);
    const rootRelease = !version && versions.find((entry) => entry.version !== 'main');
    const latest = rootRelease && relativePath.startsWith('es/') && rootRelease;
    if (!historical && !latest && !sharedPages) return;
    const root = state.env.path?.slice(0, -relativePath.length);
    const original = root && state.env.realPath?.startsWith(root) ? state.env.realPath.slice(root.length) : relativePath;
    const moved = original.slice(0, original.lastIndexOf('/') + 1) !== relativePath.slice(0, relativePath.lastIndexOf('/') + 1);
    function rebase(href) {
      if (!moved || !href || /^(?:[a-z][a-z\d+.-]*:|\/|#|\?)/i.test(href)) return href;
      const url = new URL(href, `https://docs.invalid/${original}`);
      return url.pathname + url.search + url.hash;
    }
    function rewriteLink(href, rawHTML = false) {
      if (latest && href) {
        const url = new URL(href, `https://docs.invalid/${relativePath}`);
        if (url.hostname === 'docs.invalid' && /^\/es\/(?:docs|guide)(\/|\.|$)/.test(url.pathname)
          && !pageForLink(url.pathname, latest.pages) && pageForLink(url.pathname.slice(3), latest.pages)) {
          href = url.pathname.slice(3) + url.search + url.hash;
        }
      }
      href = historical ? historicalLink(rebase(href), version, rawHTML) : href;
      if (rootRelease && href) {
        const url = new URL(href, `https://docs.invalid/${relativePath}`);
        const landing = url.pathname.match(/^\/((?:es\/)?(?:docs|guide))\/(?:index\.(?:md|html))?$/);
        if (url.hostname === 'docs.invalid' && landing && !rootRelease.pages.includes(`${landing[1]}.md`)
          && rootRelease.pages.includes(`${landing[1]}/index.md`)) return `/${landing[1]}.${rawHTML ? 'html' : 'md'}${url.search}${url.hash}`;
      }
      if (sharedPages) validateSharedLink(href, sharedPages, relativePath);
      return href;
    }
    function rewrite(tokens) {
      for (const token of tokens) {
        if (token.type === 'link_open') {
          const href = rewriteLink(token.attrGet('href'));
          token.attrSet('href', href);
          if (version && sharedSection.test(href)) token.attrSet('target', '_self');
        }
        if (historical && token.type === 'image') token.attrSet('src', rebase(token.attrGet('src')));
        if (token.type === 'html_inline' || token.type === 'html_block') {
          // Older Azure instructions used unescaped URL placeholders in prose.
          if (historical) token.content = token.content.replace(/<(organization|project)>/g, '&lt;$1&gt;');
          token.content = token.content.replace(/\bhref=(['"])(.*?)\1/g, (_, quote, href) => `href=${quote}${rewriteLink(href, true)}${quote}`);
          if (historical && moved) token.content = token.content.replace(/\bsrc=(['"])(.*?)\1/g, (_, quote, src) => `src=${quote}${rebase(src)}${quote}`);
        }
        if (token.children) rewrite(token.children);
      }
    }
    rewrite(state.tokens);
  });
}

function pageForLink(link, pages) {
  const path = link.split(/[?#]/)[0].replace(/^\//, '').replace(/\.(html|md)$/, '');
  return pages.includes(`${path}.md`) || pages.includes(`${path.replace(/\/$/, '')}/index.md`);
}

export function validateSharedLink(href, pages, source) {
  if (!href || !sharedSection.test(href)) return;
  let pathname;
  try {
    pathname = decodeURIComponent(new URL(href, 'https://docs.invalid').pathname);
  } catch (cause) {
    throw new Error(`Invalid current-site link ${href} in ${source}`, { cause });
  }
  if (!pages.includes(pathname.slice(1)) && !pageForLink(pathname, pages)) {
    throw new Error(`Broken current-site link ${href} in ${source}`);
  }
}

export function historicalSidebar(items, pages) {
  return items.flatMap((item) => {
    const children = item.items && historicalSidebar(item.items, pages);
    const candidate = item.link?.startsWith('/es/') && !pageForLink(item.link, pages) ? item.link.slice(3) : item.link;
    let link = candidate && pageForLink(candidate, pages) ? candidate : undefined;
    if (link && !/[.#?]|\/$/.test(link) && !pages.includes(`${link.slice(1)}.md`)) link += '/';
    if (!link && !children?.length) return [];
    return [{ ...item, link, ...(children ? { items: children } : {}) }];
  });
}

export function configureVersions(config, version, versions, sidebar, metadata = {}) {
  if (metadata.sharedPages) {
    const check = (items) => items?.forEach((item) => {
      validateSharedLink(item.link, metadata.sharedPages, 'sidebar');
      check(item.items);
    });
    check(sidebar);
    for (const locale of Object.values(config.locales)) check(locale.themeConfig?.sidebar);
  }
  config.themeConfig.docVersions = versions.map(({ version }) => ({ version }));
  config.themeConfig.docVersion = version;
  if (!versions.length) return config;
  const latest = versions.find((entry) => entry.version !== 'main');
  const transformPageData = config.transformPageData;
  config.transformPageData = async (page, context) => {
    Object.assign(page, await transformPageData?.(page, context));
    const documentation = /^(?:es\/)?(?:docs|guide)(\/|\.md$)/.test(page.relativePath);
    const ref = version || (documentation ? latest.version : 'main');
    const source = version || documentation ? metadata.sources?.[ref]?.[page.relativePath] ?? metadata.sources?.[ref]?.[page.filePath] : page.filePath;
    if (version) {
      if (source) page.sourceURL = `https://github.com/runatlantis/atlantis/blob/${ref}/runatlantis.io/${source}`;
      else page.frontmatter.editLink = false;
    }
    if (!version && page.frontmatter.lastUpdated !== false && !(page.frontmatter.lastUpdated instanceof Date)) {
      const timestamp = metadata.lastUpdated?.[ref]?.[source];
      if (timestamp) page.lastUpdated = timestamp;
    }
    if (!version && documentation) page.filePath = metadata.sources?.main?.[page.relativePath] ?? page.filePath;
  };
  if (!version) {
    if (latest && !latest.pages.some((page) => page.startsWith('es/docs/'))) config.themeConfig.i18nRouting = false;
    for (const section of ['docs', 'guide', 'es/docs', 'es/guide']) {
      if (latest && !latest.pages.includes(`${section}.md`) && latest.pages.includes(`${section}/index.md`)) {
        config.rewrites = { ...config.rewrites, [`${section}/index.md`]: `${section}.md` };
      }
    }
    const rootPages = latest?.pages.map((page) => config.rewrites?.[page] ?? page);
    if (latest) config.locales.root.themeConfig.sidebar = sidebar.flatMap((item) =>
      sharedSection.test(item.link ?? '') ? [item] : historicalSidebar([item], rootPages));
    if (latest && config.locales.es) config.locales.es.themeConfig.sidebar = config.locales.es.themeConfig.sidebar.flatMap((item) =>
      sharedSection.test(item.link ?? '') ? [item] : historicalSidebar([item], rootPages));
    return config;
  }
  const release = versions.find((entry) => entry.version === version);
  if (!release) throw new Error(`missing documentation manifest for ${version}`);
  config.base = versionBase(version);
  config.title = `Atlantis ${version}`;
  config.themeConfig.siteTitle = 'Atlantis';
  config.lastUpdated = false;
  config.themeConfig.sharedSidebar = sidebar.filter((item) => sharedSection.test(item.link ?? ''));
  config.themeConfig.editLink = {
    pattern: ({ sourceURL }) => sourceURL || '',
    text: version === 'main' ? 'Browse main source' : 'Browse release source',
  };
  const spanish = config.locales.es;
  config.locales = { root: { label: 'English', lang: 'en-US', themeConfig: {
    nav: [{ text: 'Documentation', link: release.pages.includes('docs.md') ? '/docs' : '/docs/' }],
    sidebar: historicalSidebar(sidebar.filter((item) => !sharedSection.test(item.link ?? '')), release.pages),
  } } };
  if (spanish && release.pages.some((page) => page.startsWith('es/docs/'))) {
    config.locales.es = { ...spanish, themeConfig: {
      ...spanish.themeConfig,
      editLink: config.themeConfig.editLink,
      nav: [{ text: 'Documentación', link: release.pages.includes('es/docs.md') ? '/es/docs' : '/es/docs/' }],
      sidebar: historicalSidebar(spanish.themeConfig.sidebar.filter((item) => !sharedSection.test(item.link ?? '')), release.pages),
      sharedSidebar: spanish.themeConfig.sidebar.filter((item) => sharedSection.test(item.link ?? '')),
    } };
  }
  config.themeConfig.search = { provider: 'local' };
  config.head = config.head.map(([tag, attributes, ...rest]) => [tag, Object.fromEntries(
    Object.entries(attributes).map(([key, value]) => [key,
      ['href', 'src', 'content'].includes(key) && typeof value === 'string' && value.startsWith('/') && !value.startsWith('//')
        ? versionBase(version) + value.slice(1) : value]),
  ), ...rest]);
  config.head.push(['meta', { name: 'robots', content: 'noindex, follow' }]);
  config.buildEnd = undefined;
  return config;
}

export function versionTarget(relativePath, version, versions) {
  const pages = versions.find((entry) => entry.version === version)?.pages;
  if (!pages?.includes(relativePath) && relativePath.startsWith('es/')) {
    const english = relativePath.slice(3);
    if (pages?.includes(english) || pages?.includes(english.replace(/\.md$/, '/index.md'))) relativePath = english;
  }
  const landing = relativePath.match(/^((?:es\/)?(?:docs|guide))(\/index)?\.md$/);
  if (!pages?.includes(relativePath) && landing) {
    const alternate = landing[2] ? `${landing[1]}.md` : `${landing[1]}/index.md`;
    if (pages?.includes(alternate)) relativePath = alternate;
  }
  const missingPage = Boolean(version && !pages?.includes(relativePath));
  const page = missingPage ? 'docs/server-configuration.md' : relativePath;
  return {
    path: versionBase(version) + page.replace(/(^|\/)index\.md$/, '$1').replace(/\.md$/, '.html'),
    missingPage,
  };
}
