/** What the site calls itself (site_name in the server's configuration). */
export const siteName =
  (typeof document !== 'undefined' &&
    document.querySelector<HTMLMetaElement>('meta[name="application-name"]')?.content) ||
  'filebrowser';
