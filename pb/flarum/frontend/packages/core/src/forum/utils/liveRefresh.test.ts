/**
 * The live-refresh registry.
 *
 * The forum has no realtime transport, so an open page revalidates on an
 * interval and whenever the tab comes back. That is the behaviour pinned here,
 * because every failure mode of it is SILENT: a refresher that stays registered
 * after its page is gone keeps polling behind the reader's back; a tick that
 * fires in a hidden tab burns requests nobody sees; a refreshed page that is
 * never redrawn looks like “nothing happened”.
 */
// Imported rather than taken from the injected globals: this file is
// type-checked (`npm run typecheck`), and the ambient Jest types are not in
// scope for the TS build.
import { afterEach, beforeEach, describe, expect, it, jest } from '@jest/globals';

import { LIVE_REFRESH_INTERVAL, refreshNow, registerLiveRefresh, unregisterLiveRefresh, type LiveRefresher } from './liveRefresh';

/** Set `document.hidden`, which jsdom exposes as a read-only getter. */
function setHidden(hidden: boolean): void {
  Object.defineProperty(document, 'hidden', { configurable: true, value: hidden });
}

/**
 * The registrations a test made, so `afterEach` can drop them.
 *
 * The registry is module state: a refresher left behind would leak into the
 * next test — and into its timer count.
 */
let registered: Array<() => void> = [];

function register(refresher: LiveRefresher): () => void {
  const unregister = registerLiveRefresh(refresher);

  registered.push(unregister);

  return unregister;
}

describe('live refresh', () => {
  beforeEach(() => {
    jest.useFakeTimers();
    setHidden(false);
  });

  afterEach(() => {
    while (registered.length) registered.pop()!();

    jest.useRealTimers();
  });

  it('runs every registered refresher', () => {
    const first = jest.fn(() => {});
    const second = jest.fn(() => {});

    register(first);
    register(second);

    refreshNow();

    expect(first).toHaveBeenCalledTimes(1);
    expect(second).toHaveBeenCalledTimes(1);
  });

  it('stops refreshing a page that has been unregistered', () => {
    const refresher = jest.fn(() => {});
    const unregister = register(refresher);

    unregister();
    refreshNow();

    expect(refresher).not.toHaveBeenCalled();
  });

  it('uses ONE interval for any number of refreshers', () => {
    register(jest.fn(() => {}));
    register(jest.fn(() => {}));
    register(jest.fn(() => {}));

    // One interval, not three: three pages' worth of timers would drift apart
    // and hammer the API in bursts.
    expect(jest.getTimerCount()).toBe(1);
  });

  it('ticks on the interval while the page is visible', () => {
    const refresher = jest.fn(() => {});
    register(refresher);

    jest.advanceTimersByTime(LIVE_REFRESH_INTERVAL);

    expect(refresher).toHaveBeenCalledTimes(1);

    jest.advanceTimersByTime(LIVE_REFRESH_INTERVAL);

    expect(refresher).toHaveBeenCalledTimes(2);
  });

  it('does nothing while the tab is hidden', () => {
    const refresher = jest.fn(() => {});
    register(refresher);

    setHidden(true);
    jest.advanceTimersByTime(LIVE_REFRESH_INTERVAL * 3);

    expect(refresher).not.toHaveBeenCalled();
  });

  it('refreshes as soon as the tab comes back', () => {
    const refresher = jest.fn(() => {});
    register(refresher);

    setHidden(true);
    jest.advanceTimersByTime(LIVE_REFRESH_INTERVAL);
    expect(refresher).not.toHaveBeenCalled();

    // A reader returning to an idle tab is exactly who is looking at stale
    // content, so the return is its own trigger.
    setHidden(false);
    document.dispatchEvent(new Event('visibilitychange'));

    expect(refresher).toHaveBeenCalledTimes(1);
  });

  it('ignores a visibility change that hides the tab', () => {
    const refresher = jest.fn(() => {});
    register(refresher);

    setHidden(true);
    document.dispatchEvent(new Event('visibilitychange'));

    expect(refresher).not.toHaveBeenCalled();
  });

  it('keeps refreshing the other pages when one throws', () => {
    const broken = jest.fn(() => {
      throw new Error('this page is broken');
    });
    const healthy = jest.fn(() => {});

    register(broken);
    register(healthy);

    expect(() => refreshNow()).not.toThrow();
    expect(healthy).toHaveBeenCalledTimes(1);
  });

  it('clears the interval when the last refresher leaves', () => {
    const unregister = register(jest.fn(() => {}));

    expect(jest.getTimerCount()).toBe(1);

    unregister();

    expect(jest.getTimerCount()).toBe(0);
  });

  it('accepts an unregister that the caller repeats', () => {
    const refresher = jest.fn(() => {});
    const unregister = register(refresher);

    unregister();
    unregister();

    refreshNow();

    expect(refresher).not.toHaveBeenCalled();
  });

  it('waits for an asynchronous refresher and swallows its failure', async () => {
    const settled = jest.fn(() => Promise.resolve());
    const failed = jest.fn(() => Promise.reject(new Error('request failed')));

    register(settled);
    register(failed);

    refreshNow();

    // Let the microtask queue drain; the rejection must not surface as an
    // unhandled rejection (which is how a poll loop takes a page down).
    await Promise.resolve();
    await Promise.resolve();

    expect(settled).toHaveBeenCalledTimes(1);
    expect(failed).toHaveBeenCalledTimes(1);
  });

  it('unregisters through the exported function too', () => {
    const refresher = jest.fn(() => {});

    register(refresher);
    unregisterLiveRefresh(refresher);
    refreshNow();

    expect(refresher).not.toHaveBeenCalled();
    expect(jest.getTimerCount()).toBe(0);
  });
});
