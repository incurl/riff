// The Statistics extension's forum initialiser.
//
// It declares NOTHING, and that is upstream's behaviour: `flarum/statistics`
// ships an admin bundle only (`new Extend\Frontend('admin')` in its
// `extend.php`). The file exists because the extension build emits both
// bundles and the Go plugin's asset bundle names the paths — a missing
// `forum.js` would be a 404 in the forum shell for an extension that has
// nothing to do there.
export const extend = [];
