// The Markdown extension's forum initialiser.
//
// It mirrors the toolbar half of upstream `flarum/markdown`: a row of
// formatting buttons in the composer. The FORMATTER half is not here — a
// post is rendered server-side by riff's own markdown engine
// (markdown/markdown.go via pb/flarum/api/service.go), which is what makes
// this plugin thin rather than a second implementation.
//
// The buttons wrap the current selection, or insert a placeholder when
// nothing is selected. They drive the `<textarea>` directly because that is
// what the vendored TextEditor binds: rewriting `value` and dispatching an
// `input` event is how any controlled component picks the change up, and it
// avoids reaching into the component's internals for a setter it does not
// expose.
//
// NOTHING may touch `app.forum` or `app.session` at module scope: the shell
// boots the extensions before `app.boot()` assigns them.
import { extend as extendMethod } from 'flarum/common/extend';
import TextEditor from 'flarum/common/components/TextEditor';
import Button from 'flarum/common/components/Button';

/**
 * The buttons, in the order upstream's toolbar shows them.
 *
 * `prefix`/`suffix` wrap the selection; `placeholder` is inserted when the
 * selection is empty so the caret lands between the markers. `linePrefix`
 * applies to the start of the line instead, which is how a list or a quote
 * works.
 */
const BUTTONS = [
  { key: 'bold', icon: 'fas fa-bold', title: 'Add bold text', prefix: '**', suffix: '**', placeholder: 'bold text' },
  { key: 'italic', icon: 'fas fa-italic', title: 'Add italic text', prefix: '*', suffix: '*', placeholder: 'italic text' },
  {
    key: 'strikethrough',
    icon: 'fas fa-strikethrough',
    title: 'Add strikethrough text',
    prefix: '~~',
    suffix: '~~',
    placeholder: 'struck text',
  },
  { key: 'code', icon: 'fas fa-code', title: 'Insert code', prefix: '`', suffix: '`', placeholder: 'code' },
  { key: 'quote', icon: 'fas fa-quote-left', title: 'Insert a quote', linePrefix: '> ', placeholder: 'quote' },
  { key: 'header', icon: 'fas fa-heading', title: 'Add header text', linePrefix: '## ', placeholder: 'header' },
  { key: 'unordered_list', icon: 'fas fa-list-ul', title: 'Add a bulleted list', linePrefix: '- ', placeholder: 'item' },
  { key: 'ordered_list', icon: 'fas fa-list-ol', title: 'Add a numbered list', linePrefix: '1. ', placeholder: 'item' },
  { key: 'link', icon: 'fas fa-link', title: 'Add a link', prefix: '[', suffix: '](https://)', placeholder: 'link text' },
  { key: 'image', icon: 'fas fa-image', title: 'Add an image', prefix: '![', suffix: '](https://)', placeholder: 'alt text' },
  // The spoiler is the syntax this port ADDED to riff's markdown
  // (markdown/extensions/spoiler.go), and upstream has it too.
  { key: 'spoiler', icon: 'fas fa-eye-slash', title: 'Insert a spoiler', prefix: '||', suffix: '||', placeholder: 'hidden text' },
];

/**
 * Replace the textarea's selection with the wrapped text.
 */
function apply(textarea, button) {
  const start = textarea.selectionStart;
  const end = textarea.selectionEnd;
  const value = textarea.value;
  const selected = value.slice(start, end);

  let next;
  let caretFrom;

  if (button.linePrefix) {
    const from = value.lastIndexOf('\n', start - 1) + 1;
    next = value.slice(0, from) + button.linePrefix + value.slice(from);
    caretFrom = start + button.linePrefix.length;
  } else {
    const body = selected || button.placeholder;
    next = value.slice(0, start) + button.prefix + body + button.suffix + value.slice(end);
    caretFrom = start + button.prefix.length;
  }

  textarea.value = next;
  textarea.dispatchEvent(new Event('input', { bubbles: true }));
  textarea.focus();

  if (button.linePrefix) {
    textarea.setSelectionRange(caretFrom, caretFrom);
  } else {
    textarea.setSelectionRange(caretFrom, caretFrom + (selected || button.placeholder).length);
  }
}

export const extend = [
  {
    extend() {
      extendMethod(TextEditor.prototype, 'toolbarItems', function (items) {
        const buttons = BUTTONS.map((button) => (
          <Button
            type="button"
            className="Button Button--link MarkdownToolbar-button"
            icon={button.icon}
            title={button.title}
            aria-label={button.title}
            onclick={(event) => {
              event.preventDefault();

              // The textarea is the toolbar's sibling inside the editor.
              const editor = event.target.closest('.TextEditor');
              const textarea = editor && editor.querySelector('textarea');

              if (textarea) apply(textarea, button);
            }}
          />
        ));

        items.add('markdown-toolbar', <span className="MarkdownToolbar">{buttons}</span>, 100);
      });
    },
  },
];
