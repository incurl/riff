import AlertManager from './AlertManager';
import AlertManagerState from '../states/AlertManagerState';
import m from 'mithril';

/**
 * An alert must be DISMISSIBLE.
 *
 * The admin application shows its alerts through the `AlertManager`,
 * which mounts into the shell's `#alerts` element. A dismiss button
 * that does nothing leaves the alert on screen forever, and the
 * operator has no way to clear it.
 *
 * The dismissal has TWO halves, and both must happen:
 *
 *   - the manager's bookkeeping — the alert leaves the list, so it is
 *     not drawn again;
 *   - the caller's own `ondismiss`, which an extension may have
 *     registered to hear that its alert was dismissed.
 *
 * The manager WRAPS the caller's handler rather than replacing it: a
 * handler that only called the caller's would leave the alert in the
 * list, and one that only called the manager's would swallow the
 * extension's notification.
 */
describe('AlertManager', () => {
  /**
   * Mount the manager and show one alert.
   */
  const render = async (attrs: Record<string, unknown> = {}) => {
    const container = document.createElement('div');
    document.body.appendChild(container);

    const state = new AlertManagerState();

    m.mount(container, {
      view: () => m(AlertManager, { state }),
    });

    state.show({ type: 'error', ...attrs }, 'Something went wrong');

    // The state shows the alert with `m.redraw()`, and Mithril's
    // redraw is SCHEDULED — through `requestAnimationFrame`, which
    // jsdom does not flush on a timer. `m.redraw.sync()` runs the
    // pending redraw immediately, so the assertion does not depend on
    // the animation frame.
    m.redraw.sync();

    return { container, state };
  };

  /**
   * The dismiss button the alert carries.
   */
  const dismissButton = (container: HTMLElement) =>
    container.querySelector<HTMLButtonElement>('.Alert-dismiss');

  it('shows the alert', async () => {
    const { container } = await render();

    expect(container.querySelector('.Alert')).not.toBeNull();
    expect(container.textContent).toContain('Something went wrong');
  });

  it('carries a dismiss button', async () => {
    const { container } = await render();

    expect(dismissButton(container)).not.toBeNull();
  });

  it('removes the alert when the dismiss button is clicked', async () => {
    const { container, state } = await render();

    dismissButton(container)!.click();
    m.redraw.sync();

    // The manager's bookkeeping: the alert leaves the list.
    expect(state.getActiveAlerts()).toEqual({});
    expect(container.querySelector('.Alert')).toBeNull();
  });

  it("calls the caller's own ondismiss as well", async () => {
    let called = 0;

    const { container } = await render({
      ondismiss: () => {
        called++;
      },
    });

    dismissButton(container)!.click();

    // The extension's notification: it hears that its alert was
    // dismissed.
    expect(called).toBe(1);
  });

  it('leaves a non-dismissible alert alone', async () => {
    const { container, state } = await render({ dismissible: false });

    // The button is not rendered at all, so there is nothing to click.
    expect(dismissButton(container)).toBeNull();
    expect(Object.keys(state.getActiveAlerts())).toHaveLength(1);
  });
});
