/** File extension (or whole name) → highlight.js language. */
const byExt: Record<string, string> = {
  js: 'javascript',
  mjs: 'javascript',
  cjs: 'javascript',
  jsx: 'javascript',
  ts: 'typescript',
  mts: 'typescript',
  cts: 'typescript',
  tsx: 'typescript',
  json: 'json',
  jsonc: 'json',
  webmanifest: 'json',
  html: 'xml',
  htm: 'xml',
  xml: 'xml',
  svg: 'xml',
  xhtml: 'xml',
  svelte: 'xml',
  vue: 'xml',
  plist: 'xml',
  css: 'css',
  scss: 'scss',
  less: 'less',
  md: 'markdown',
  markdown: 'markdown',
  py: 'python',
  pyw: 'python',
  rb: 'ruby',
  php: 'php',
  pl: 'perl',
  pm: 'perl',
  lua: 'lua',
  r: 'r',
  go: 'go',
  rs: 'rust',
  c: 'c',
  h: 'c',
  cpp: 'cpp',
  cc: 'cpp',
  cxx: 'cpp',
  hpp: 'cpp',
  hh: 'cpp',
  cs: 'csharp',
  java: 'java',
  kt: 'kotlin',
  kts: 'kotlin',
  swift: 'swift',
  m: 'objectivec',
  mm: 'objectivec',
  sh: 'bash',
  bash: 'bash',
  zsh: 'bash',
  fish: 'bash',
  ps1: 'powershell',
  bat: 'dos',
  cmd: 'dos',
  sql: 'sql',
  yaml: 'yaml',
  yml: 'yaml',
  toml: 'ini',
  ini: 'ini',
  cfg: 'ini',
  conf: 'ini',
  env: 'ini',
  service: 'ini',
  timer: 'ini',
  diff: 'diff',
  patch: 'diff',
  graphql: 'graphql',
  gql: 'graphql',
  vb: 'vbnet',
  wasm: 'wasm',
  wat: 'wasm',
  mk: 'makefile',
};

const byName: Record<string, string> = {
  makefile: 'makefile',
  gnumakefile: 'makefile',
  dockerfile: 'bash',
  '.bashrc': 'bash',
  '.zshrc': 'bash',
  '.profile': 'bash',
  '.gitconfig': 'ini',
  '.editorconfig': 'ini',
  '.npmrc': 'ini',
};

export function languageOf(name: string): string | undefined {
  const lower = name.toLowerCase();
  if (byName[lower]) return byName[lower];
  const i = lower.lastIndexOf('.');
  return i < 0 ? undefined : byExt[lower.slice(i + 1)];
}

/** Above this, text is shown without highlighting (it would stall the page). */
export const HIGHLIGHT_MAX = 512 * 1024;

/**
 * Returns highlighted HTML for text, or null when there is no known language.
 * highlight.js escapes its input: the result holds only its own <span>s
 * around escaped text, so it is safe to insert as HTML.
 */
export async function highlight(name: string, text: string): Promise<string | null> {
  const lang = languageOf(name);
  if (!lang || text.length > HIGHLIGHT_MAX) return null;
  const { default: hljs } = await import('highlight.js/lib/common');
  if (!hljs.getLanguage(lang)) return null;
  return hljs.highlight(text, { language: lang, ignoreIllegals: true }).value;
}
