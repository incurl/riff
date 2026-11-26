// The Sticky extension's admin initialiser.
//
// It registers the ONE setting the server reads
// (`flarum-sticky.pin_sticky_on_all_discussions`), on the extension's own
// settings page. Upstream's page carries three; the other two are not
// declared because nothing here honours them — `enable_display_excerpt`
// needs the `firstPostExcerpt` attribute (not ported) and
// `only_sticky_unread_discussions` needs a per-user read-state table (this
// port keeps none), and a switch that changes nothing is worse than an
// absent one.
//
// The label is a plain string, for the reason the likes bundle gives: this
// port vendors no upstream extension locale files, so a translation key
// would render as the key itself.
import Extend from 'flarum/common/extenders';

export const extend = [
  new Extend.Admin().setting(() => ({
    setting: 'flarum-sticky.pin_sticky_on_all_discussions',
    type: 'bool',
    label: 'Pin pinned discussions to the top',
    help: 'Show pinned discussions first in the All Discussions list. Turn this off to leave them in date order; a tag page always shows its pinned discussions first.',
  })),
];
