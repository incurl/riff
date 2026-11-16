import ColorPreviewInput from './ColorPreviewInput';
import m from 'mithril';

/**
 * The preview swatch is an `<input type="color">`, and that element's
 * value sanitization algorithm is "value = a valid CSS color". An EMPTY
 * value is not a valid CSS color, so assigning one throws:
 *
 *   The specified value "" does not conform to the required format.
 *   The value must be a valid CSS color.
 *
 * Mithril assigns the attribute as a DOM property (`vnode.dom[key] =
 * value`), so an empty value reaches the browser's validator and the
 * error surfaces during a redraw — which is why it appears on a
 * `m.redraw()` and not on the action that triggered it.
 *
 * An empty colour is a legitimate state (it means "no colour set"), and
 * the TEXT input keeps it. The preview swatch cannot, so it falls back
 * to a neutral grey.
 */
describe('ColorPreviewInput', () => {
  /**
   * Render the component and report the value ASSIGNED to the preview
   * input.
   *
   * The assignment is what the browser validates, so that is what the
   * test has to observe: jsdom SILENTLY coerces an invalid colour input
   * value to "#000000" instead of throwing the way a browser does, so
   * reading `input.value` back cannot tell the two apart.
   */
  const render = async (value: string) => {
    const container = document.createElement('div');
    document.body.appendChild(container);

    const assigned: string[] = [];
    const descriptor = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!;
    const setValue = descriptor.set!;

    Object.defineProperty(window.HTMLInputElement.prototype, 'value', {
      configurable: true,
      get: descriptor.get,
      set(this: HTMLInputElement, next: string) {
        assigned.push(next);
        setValue.call(this, next);
      },
    });

    try {
      m.render(container, m(ColorPreviewInput, { value }));

      // Mithril renders asynchronously, so the DOM is only populated
      // after the redraw.
      await new Promise((resolve) => setTimeout(resolve, 0));
    } finally {
      Object.defineProperty(window.HTMLInputElement.prototype, 'value', descriptor);
    }

    const preview = container.querySelector('input[type="color"]') as HTMLInputElement;
    const text = container.querySelector('.FormControl') as HTMLInputElement;

    return { assigned, preview, text };
  };

  it('gives the preview swatch a valid CSS colour when the value is empty', async () => {
    const { assigned } = await render('');

    // The last assignment is the preview swatch's (the text input is
    // assigned first).
    const previewValue = assigned[assigned.length - 1];

    // An empty value is not a valid CSS colour, and the browser refuses
    // it outright.
    expect(previewValue).not.toBe('');
    expect(/^#[a-f0-9]{6}$/i.test(previewValue)).toBe(true);
  });

  it('keeps the empty value on the text input', async () => {
    // Clearing the field is a valid "no colour" state and must survive:
    // coercing it to a colour would silently change the setting.
    const { text } = await render('');

    expect(text.value).toBe('');
  });

  it('passes a set colour through to both inputs', async () => {
    const { preview, text } = await render('#4d698e');

    expect(preview.value).toBe('#4d698e');
    expect(text.value).toBe('#4d698e');
  });
});
