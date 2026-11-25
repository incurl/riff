// The Tags extension's admin initialiser.
//
// Upstream's `flarum/tags` uses a CUSTOM PAGE (a tag tree plus a
// "Settings" tab), not the plain settings list. This port registers
// only the SETTINGS half of that page — the four count settings —
// because the tag tree itself is a separate feature this port does not
// carry.
//
// The four keys are upstream's own:
// `flarum-tags.min_primary_tags`, `flarum-tags.max_primary_tags`,
// `flarum-tags.min_secondary_tags` and
// `flarum-tags.max_secondary_tags`.
//
// Upstream renders them as a RANGE input (a minimum and a maximum side
// by side, where raising the minimum raises the maximum with it). They
// are registered here as four separate number fields, so the
// min/max relationship is NOT enforced by the form — the SERVER
// enforces it, and it does so on the whole set rather than on one
// attachment (see `pb/flarum/plugins/tags/counts.go`).
//
// The label and the help text are plain strings rather than
// `app.translator.trans(...)` calls: this port vendors no upstream
// extension locale files, so a translation key would render as the
// key itself.
import Extend from 'flarum/common/extenders';

export const extend = [
  new Extend.Admin()
    .section(() => ({
      id: 'tag_counts',
      icon: 'fas fa-tags',
      label: 'Tag counts',
      description: 'How many tags a discussion must and may carry.',
    }))
    .setting(() => ({
      setting: 'flarum-tags.min_primary_tags',
      section: 'tag_counts',
      type: 'number',
      label: 'Minimum primary tags',
      help: 'The least number of primary tags a discussion must carry.',
      min: 0,
    }))
    .setting(() => ({
      setting: 'flarum-tags.max_primary_tags',
      section: 'tag_counts',
      type: 'number',
      label: 'Maximum primary tags',
      help: 'The most number of primary tags a discussion may carry.',
      min: 0,
    }))
    .setting(() => ({
      setting: 'flarum-tags.min_secondary_tags',
      section: 'tag_counts',
      type: 'number',
      label: 'Minimum secondary tags',
      help: 'The least number of secondary tags a discussion must carry.',
      min: 0,
    }))
    .setting(() => ({
      setting: 'flarum-tags.max_secondary_tags',
      section: 'tag_counts',
      type: 'number',
      label: 'Maximum secondary tags',
      help: 'The most number of secondary tags a discussion may carry.',
      min: 0,
    })),
];
