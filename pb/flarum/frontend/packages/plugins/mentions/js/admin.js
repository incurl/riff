// The Mentions extension's admin entry point.
//
// Upstream's `flarum/mentions` registers ONE setting on its extension
// page: `flarum-mentions.allow_username_format`, a boolean that turns
// the bare `@username` form off.
//
// It is NOT registered here, and that is a decision rather than an
// omission. Upstream's setting gates the BARE form only — the quoted
// `@"Display Name"#id` form still resolves — so turning it off leaves
// the other form.
//
// This port recognises the BARE form only: the quoted form is out of
// scope (see `docs/design/flarum/mentions.md`), because riff's
// usernames are `[a-z0-9_-]{3,30}` and never need quoting. So turning
// the setting off here would leave NO way to mention at all — and a
// control that removes a feature entirely is not one an administrator
// should be handed.
//
// The entry exists (the webpack config builds every entry it finds) and
// contributes nothing. The admin bundle is still served, so the
// manifest's `admin` URL is not a 404.
//
// A named export, not a default: the boot shell reads
// `flarum.extensions[id].extend`, so a default export would sit
// behind a `default` key and never be seen.
export const extend = [];
