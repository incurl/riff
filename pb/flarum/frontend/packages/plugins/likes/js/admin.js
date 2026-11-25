// The Likes extension's admin initialiser.
//
// Upstream's `flarum/likes` registers ONE setting on its extension
// page: `flarum-likes.like_own_post`, a boolean in the `liking`
// section. The key and the meaning are upstream's, so an operator's
// saved setting carries over.
//
// The setting is a BOOLEAN and upstream's default is ON, so the forum
// allows liking one's own post unless an operator turns it off. The
// server reads the same key (`pb/flarum/plugins/likes/policy.go`) and
// falls back to the same default, so the switch here and the forum's
// behaviour agree.
//
// The label and the help text are plain strings rather than
// `app.translator.trans(...)` calls: this port vendors no upstream
// extension locale files, so a translation key would render as the
// key itself.
import Extend from 'flarum/common/extenders';

export const extend = [
  new Extend.Admin()
    .section(() => ({
      id: 'liking',
      icon: 'fas fa-thumbs-up',
      label: 'Liking',
      description: 'Allow members to like posts, and who may like their own.',
    }))
    .setting(() => ({
      setting: 'flarum-likes.like_own_post',
      section: 'liking',
      type: 'bool',
      label: 'Allow liking your own posts',
      help: 'When enabled, a member may like their own post. Disable it to stop self-likes.',
    })),
];
