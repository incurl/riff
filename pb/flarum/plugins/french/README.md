# French language pack (`fr`)

The forum's French translations, vendored from
[flarum-lang/french](https://github.com/flarum-lang/french) (MIT, see
`LICENSE`).

- Upstream branch: `master`
- Upstream commit: `b36648e159c83b479ea15706c710400c6e2ccbf5`
- Locale code: `fr`

## What is vendored

Only the catalogue files for the extensions this port actually serves:

| File | Namespace | Used by |
| --- | --- | --- |
| `locale/core.yml` | `core` | the forum and admin apps |
| `locale/validation.yml` | `validation` | form validation messages |
| `locale/flarum-tags.yml` | `flarum-tags` (and `flarum-audit`) | the Tags plugin |
| `locale/flarum-likes.yml` | `flarum-likes` | the Likes plugin |
| `locale/flarum-lock.yml` | `flarum-lock` | the Lock plugin |
| `locale/flarum-sticky.yml` | `flarum-sticky` | the Sticky plugin |
| `locale/flarum-suspend.yml` | `flarum-suspend` | the Suspend plugin |
| `locale/flarum-mentions.yml` | `flarum-mentions` | the Mentions plugin |
| `locale/flarum-markdown.yml` | `flarum-markdown` | the Markdown plugin |
| `locale/flarum-statistics.yml` | `flarum-statistics` | the Statistics plugin |
| `locale/fof-upload.yml` | `fof-upload` (and `blomstra-gdpr`) | the Uploads plugin |
| `locale/fof-polls.yml` | `fof-polls` | the Polls plugin |
| `locale/fof-gamification.yml` | `fof-gamification` | the Gamification plugin |

The upstream repository carries translations for several hundred other
extensions. They are deliberately not vendored: each one is a namespace
nothing here renders, and copying them would make the tree look like it
supports extensions this port does not have. Adding one is a matter of
dropping its `.yml` beside these, adding the name to `build-locale.cjs` and
re-running the build.

Upstream's own front end (`js/`, its Google-Fonts notice page, the
translation-report page) and its `icon.svg` are not ported: they configure
an upstream admin page this port has no equivalent for, and the translated
catalogue is what makes the locale work.

## How it is built

`build-locale.cjs` compiles the catalogue into the two bundles the SPA
loads, in the same shape the core's own compiler produces (a single
`app.translator.addTranslations(…)` call):

- `dist/forum-fr.js` — the `*.forum.*` and `*.lib.*` keys
- `dist/admin-fr.js` — the `*.admin.*` and `*.lib.*` keys

`scripts/build_flarum_spa.sh` runs it and stages the output into
`ui/dist/plugins/french/`, which is what the Go binary embeds.

## Using it

Two ways, and they are upstream's own:

- **The operator makes it the default**: the admin's **Basics** page, "Default
  language", writes the `default_locale` setting. That is what a reader with no
  choice of their own gets.
- **The reader picks it**: the header's language selector (shown when
  `show_language_selector` is on and more than one locale is available). A
  signed-in reader's choice is saved as the `locale` preference; a guest's is
  the `locale` cookie.

A deployment can also start from French with `RIFF_FORUM_LOCALE=fr`, which is
the fallback when the setting is unset. An unset or unknown value falls back to
`en`, so a typo never leaves the forum with no catalogue at all.

## Licence

The catalogue is upstream's, MIT, and `LICENSE` is theirs. This package adds
only the Go declaration (`french.go`) and the compiler invocation.
