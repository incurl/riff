# Chinese Simplified language pack (`zh-Hans`)

The forum's Simplified Chinese translations, vendored from
[flarum-lang/chinese-simplified](https://github.com/flarum-lang/chinese-simplified)
(MIT, see `LICENSE`).

- Upstream branch: `2.x`
- Upstream commit: `a7faf8b48e8b64f9c70e35fef12cd87efc1dafc0`
- Locale code: `zh-Hans`

## What is vendored

Only the catalogue files for the extensions this port actually serves:

| File | Namespace | Used by |
| --- | --- | --- |
| `locale/core.yml` | `core` | the forum and admin apps |
| `locale/validation.yml` | `validation` | form validation messages |
| `locale/flarum-tags.yml` | `flarum-tags` (and `flarum-audit`) | the Tags plugin |
| `locale/flarum-likes.yml` | `flarum-likes` | the Likes plugin |
| `locale/fof-upload.yml` | `fof-upload` | the Uploads plugin |

The upstream repository carries translations for several hundred other
extensions. They are deliberately not vendored: each one is a namespace
nothing here renders, and copying them would make the tree look like it
supports extensions this port does not have. Adding one is a matter of
dropping its `.yml` beside these and re-running the build.

Upstream's own front end (`js/`, the Google-Fonts notice page) and its
`less/`/`assets/` font overrides are not ported: they configure an
upstream admin page this port has no equivalent for, and the translated
catalogue is what makes the locale work.

## How it is built

`build-locale.cjs` compiles the catalogue into the two bundles the SPA
loads, in the same shape the core's own compiler produces (a single
`app.translator.addTranslations(…)` call):

- `dist/forum-zh-Hans.js` — the `*.forum.*` and `*.lib.*` keys
- `dist/admin-zh-Hans.js` — the `*.admin.*` and `*.lib.*` keys

`scripts/build_flarum_spa.sh` runs it and stages the output into
`ui/dist/plugins/chinese/`, which is what the Go binary embeds.

## Using it

Three ways, and they are upstream's own (`SetLocale` middleware):

- **The operator makes it the default**: the admin's **Basics** page, "Default
  language", writes the `default_locale` setting. That is what a reader with no
  choice of their own gets.
- **The reader picks it**: the header's language selector (shown when
  `show_language_selector` is on and more than one locale is available) saves
  the choice as the `locale` preference for a signed-in reader, or as the
  `locale` cookie for a guest.
- **The deployment starts from it**: `RIFF_FORUM_LOCALE=zh-Hans` (or
  `flarum.Options{Locale: "zh-Hans"}`) is the fallback when the setting is
  unset.

An unset or unknown value falls back to `en`, so a typo never leaves the
forum with no catalogue at all.
