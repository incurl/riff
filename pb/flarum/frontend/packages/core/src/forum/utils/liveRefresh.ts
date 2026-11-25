/**
 * Live refresh — keep an open page current without a websocket.
 *
 * The forum has no realtime transport (see `docs/design/flarum/deviations.md`:
 * upstream's `flarum/realtime` is a separate websocket daemon plus a queue,
 * and this port runs one binary). This module is the small, reversible stand-in:
 * while a page is VISIBLE it revalidates on an interval, and it revalidates the
 * moment a backgrounded tab comes back — which is the case that matters, because
 * a reader who returns to an idle tab is exactly who is looking at stale posts.
 *
 * It is a registry rather than a timer per page: one interval and one
 * `visibilitychange` listener for the whole application, and a page registers a
 * callback on mount and drops it on remove. Two pages polling would otherwise
 * mean two intervals that can drift apart.
 *
 * Deliberately NOT covered here:
 *
 *   - the notification badge. `newNotificationCount` is a CLIENT-side counter
 *     (`NotificationListState.load()` zeroes it when the list is opened), so
 *     re-reading the user record would resurrect a badge the reader has already
 *     cleared. That wants a pushed "notificationCreated" event, not a poll.
 *   - data that is not on a page (the pane, search results). They are reloaded
 *     on navigation already.
 *
 * Revalidation, not refetching: every refresher below asks the state object to
 * re-read what it is already showing, so scroll position, the open page and the
 * reader's filters survive. A failed refresh is swallowed — there is no user
 * action to report it against, and what is on screen remains the best answer.
 */

/** How often a visible page revalidates. */
export const LIVE_REFRESH_INTERVAL = 30_000;

/**
 * A page's refresh callback. It may settle later; a rejection is ignored.
 */
export type LiveRefresher = () => void | Promise<unknown>;

/** The registered refreshers, one per mounted page. */
const refreshers = new Set<LiveRefresher>();

/** The single interval, or null when nothing is registered. */
let timer: ReturnType<typeof setInterval> | null = null;

/** Whether the `visibilitychange` listener is bound. */
let visibilityBound = false;

/**
 * Register a page's refresh callback.
 *
 * @returns a function that unregisters it. Call it from the component's
 *          `onremove`, or the page keeps refreshing after it has gone.
 */
export function registerLiveRefresh(refresher: LiveRefresher): () => void {
  refreshers.add(refresher);

  if (timer === null) {
    timer = setInterval(tick, LIVE_REFRESH_INTERVAL);
  }

  if (!visibilityBound && typeof document !== 'undefined' && typeof document.addEventListener === 'function') {
    document.addEventListener('visibilitychange', onVisibilityChange);
    visibilityBound = true;
  }

  return () => unregisterLiveRefresh(refresher);
}

/**
 * Unregister a refresh callback.
 *
 * The last one to leave stops the interval, so a forum with no page open (the
 * composer is a modal, not a page) does not keep asking the API.
 */
export function unregisterLiveRefresh(refresher: LiveRefresher): void {
  refreshers.delete(refresher);

  if (refreshers.size === 0 && timer !== null) {
    clearInterval(timer);
    timer = null;
  }
}

/**
 * Whether the tab is visible.
 *
 * In a non-browser environment (a test harness) there is no `document`, and the
 * honest answer is "visible": the caller asked for a refresh, and nothing said
 * the page is hidden.
 */
export function isPageVisible(): boolean {
  return typeof document === 'undefined' || !document.hidden;
}

/**
 * Run every registered refresher now.
 *
 * A refresher that throws does not stop the others: one page's failure must not
 * cost the rest their refresh.
 */
export function refreshNow(): void {
  for (const refresher of Array.from(refreshers)) {
    try {
      const result = refresher();

      // An asynchronous refresher settles later; redraw then so the new data is
      // painted. A synchronous one is covered by the redraw below.
      if (result && typeof (result as Promise<unknown>).then === 'function') {
        (result as Promise<unknown>).then(
          () => m.redraw(),
          () => {}
        );
      }
    } catch {
      // Swallowed on purpose; see above.
    }
  }

  m.redraw();
}

/** The interval callback: refresh only while the page is actually visible. */
function tick(): void {
  if (!isPageVisible()) return;

  refreshNow();
}

/** Refresh as soon as the tab comes back into view. */
function onVisibilityChange(): void {
  if (!isPageVisible()) return;

  refreshNow();
}
