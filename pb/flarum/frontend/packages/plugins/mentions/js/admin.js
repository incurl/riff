// The Mentions extension's admin initialiser.
//
// Upstream's `flarum/mentions` registers ONE setting on its extension
// page: `flarum-mentions.allow_username_format`, a boolean in the
// `format` section.
//
// The setting gates the BARE mention form: upstream's
// `ConfigureMentions::addUserId` refuses to resolve a `@username` to an
// account when it is off, while the quoted `@"Display Name"#id` form
// still resolves. This port recognises the bare form only, so turning
// it off stops mentions from resolving at all.
//
// Upstream's installed default is 1 (the bare form resolves), and the
// server falls back to the same value
// (`pb/flarum/plugins/mentions/events.go`).
//
// The label and the help text are plain strings rather than
// `app.translator.trans(...)` calls: this port vendors no upstream
// extension locale files, so a translation key would render as the
// key itself.
import Extend from 'flarum/common/extenders';

export const extend = [
  new Extend.Admin()
    .section(() => ({
      id: 'format',
      icon: 'fas fa-at',
      label: 'Mention format',
      description: 'How a member may refer to another account.',
    }))
    .setting(() => ({
      setting: 'flarum-mentions.allow_username_format',
      section: 'format',
      type: 'bool',
      label: 'Allow the @username mention format',
      help: 'When enabled, typing @username mentions that account. Disable it to stop mentions from resolving.',
    })),
];
