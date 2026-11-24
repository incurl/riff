// The Mentions extension's admin entry point.
//
// No admin-side UI yet; the entry exists so the manifest's `admin` URL
// is not a 404.
// A named export, not a default: the boot shell reads
// `flarum.extensions[id].extend`, so a default export would sit
// behind a `default` key and never be seen.
export const extend = [];
