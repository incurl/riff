// The Tags extension's admin entry point.
//
// The plugin has no admin-side UI yet, so the entry exists (the webpack
// config builds every entry it finds) but contributes nothing. The
// admin bundle is still served, so the manifest's `admin` URL is not a
// 404.
// A named export, not a default: the boot shell reads
// `flarum.extensions[id].extend`, so a default export would sit
// behind a `default` key and never be seen.
export const extend = [];
