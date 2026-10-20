// Access token storage using localStorage for persistence across tabs and sessions.
// Tokens are cleared on logout or expiry.
import { BASE_PATH } from "./utils/base-path";

let accessToken: string | null = null;
let tokenExpiresAt: Date | null = null;

const TOKEN_KEY = "memos_access_token";
const EXPIRES_KEY = "memos_token_expires_at";

// BroadcastChannel lets tabs share freshly-refreshed tokens so that only one
// tab needs to hit the refresh endpoint. When another tab successfully refreshes
// we adopt the new token immediately, avoiding a redundant (and potentially
// conflicting) refresh request of our own.
const TOKEN_CHANNEL_NAME = "memos_token_sync";

// Token refresh policy:
// - REQUEST_TOKEN_EXPIRY_BUFFER_MS: used for normal API requests.
// - FOCUS_TOKEN_EXPIRY_BUFFER_MS: used on tab visibility restore to refresh earlier.
export const REQUEST_TOKEN_EXPIRY_BUFFER_MS = 30 * 1000;
export const FOCUS_TOKEN_EXPIRY_BUFFER_MS = 2 * 60 * 1000;

interface TokenBroadcastMessage {
  token: string;
  expiresAt: string; // ISO string
}

let tokenChannel: BroadcastChannel | null = null;

function getTokenChannel(): BroadcastChannel | null {
  if (tokenChannel) return tokenChannel;
  try {
    tokenChannel = new BroadcastChannel(TOKEN_CHANNEL_NAME);
    tokenChannel.onmessage = (event: MessageEvent<TokenBroadcastMessage>) => {
      const { token, expiresAt } = event.data ?? {};
      if (token && expiresAt) {
        // Another tab refreshed — adopt the token in-memory so we don't
        // fire our own refresh request. A cookie is per-origin rather than
        // per-tab, so re-asserting it here is what keeps this tab from
        // continuing to present the pre-rotation token to file GETs.
        accessToken = token;
        tokenExpiresAt = new Date(expiresAt);
        syncAuthCookie(token, new Date(expiresAt));
      }
    };
  } catch {
    // BroadcastChannel not available (e.g. some privacy modes)
    tokenChannel = null;
  }
  return tokenChannel;
}

// Initialize the channel at module load so the listener is registered
// before any token refresh can occur in any tab.
getTokenChannel();

export const getAccessToken = (): string | null => {
  if (!accessToken) {
    try {
      const storedToken = localStorage.getItem(TOKEN_KEY);
      const storedExpires = localStorage.getItem(EXPIRES_KEY);

      if (storedToken && storedExpires) {
        const expiresAt = new Date(storedExpires);
        if (expiresAt > new Date()) {
          accessToken = storedToken;
          tokenExpiresAt = expiresAt;
          // Re-assert the cookie on page load. It expires with the
          // token, so a reload inside a long-lived session finds no
          // cookie (or a stale one from before a token rotation), and
          // every `<img>` in the feed would fall back to anonymous.
          syncAuthCookie(storedToken, expiresAt);
        }
        // Do NOT remove expired tokens here. getRequestToken() in connect.ts calls
        // hasStoredToken() to decide whether to attempt a refresh — if we eagerly delete
        // the expired token, it returns null immediately, skipping the refresh and sending
        // the request without credentials.
        // clearAccessToken() handles proper cleanup after a confirmed auth failure or logout.
      }
    } catch (e) {
      // localStorage might not be available (e.g., in some privacy modes)
      console.warn("Failed to access localStorage:", e);
    }
  }
  return accessToken;
};

export const setAccessToken = (token: string | null, expiresAt?: Date): void => {
  accessToken = token;
  tokenExpiresAt = expiresAt || null;

  try {
    if (token && expiresAt) {
      localStorage.setItem(TOKEN_KEY, token);
      localStorage.setItem(EXPIRES_KEY, expiresAt.toISOString());
      syncAuthCookie(token, expiresAt);
      // Broadcast to other tabs so they adopt the new token without refreshing.
      const msg: TokenBroadcastMessage = { token, expiresAt: expiresAt.toISOString() };
      getTokenChannel()?.postMessage(msg);
    } else {
      localStorage.removeItem(TOKEN_KEY);
      localStorage.removeItem(EXPIRES_KEY);
      syncAuthCookie(null);
    }
  } catch (e) {
    // localStorage might not be available (e.g., in some privacy modes)
    console.warn("Failed to write to localStorage:", e);
  }
};

// The session cookie mirrors the localStorage token so that the
// browser can present a credential on requests that cannot set a
// header — above all `<img src>` / `<video poster>` / CSS `url()`,
// which is how the feed actually renders an attachment.
//
// The server (`pb/api/file_authz.go`) reads this cookie on the file
// surface only; without it a `<img>` on a PRIVATE memo — the editor's
// default — answers 404, which is the broken-image regression the
// cookie exists to fix.
//
// Three properties are load-bearing:
//
//   - `path` is scoped to the SPA's own mount path (`BASE_PATH`), so
//     the token is never sent to the static site that shares the host
//     in the `Caddyfile.fly.community` layout. On a root mount the
//     cookie is path-scoped to "/" like any site-wide cookie.
//   - `secure` follows `window.isSecureContext`, so the browser only
//     stores it under TLS (and on localhost, where isSecureContext is
//     true over plain HTTP) — the cookie is never sent in cleartext
//     to a remote host.
//   - `sameSite: "strict"` keeps the cookie off cross-site requests,
//     so a third-party page cannot ride the session.
const AUTH_COOKIE_NAME = "riff_auth";

export const syncAuthCookie = (token: string | null, expiresAt?: Date): void => {
  if (typeof document === "undefined") return;
  const path = BASE_PATH === "/" ? "/" : `${BASE_PATH}/`;
  // `Secure` is only honoured by the browser over a channel it
  // considers secure, and it is rejected outright on a plain-HTTP
  // origin (except localhost). Writing it unconditionally would make
  // the cookie a no-op on a device bring-up served over a cleartext
  // LAN address, so the attribute follows the context instead: the
  // server applies the matching rule on the way back in
  // (`fileCookieTransportAllowed`), refusing a cleartext cookie from
  // a non-loopback host.
  const secure = typeof window !== "undefined" && window.isSecureContext === false ? "" : "; Secure";
  try {
    if (token && expiresAt) {
      // Bound the cookie to the token's own lifetime so it cannot
      // outlive it and keep an expired session looking authenticated.
      document.cookie = `${AUTH_COOKIE_NAME}=${encodeURIComponent(token)}; path=${path}; expires=${expiresAt.toUTCString()}; SameSite=Strict${secure}`;
    } else {
      // An expired cookie needs an explicit past expiry to be
      // removed; the path must match the one it was written with.
      document.cookie = `${AUTH_COOKIE_NAME}=; path=${path}; expires=Thu, 01 Jan 1970 00:00:00 GMT; SameSite=Strict${secure}`;
    }
  } catch (e) {
    // A document without cookie access (a sandboxed iframe, some
    // privacy modes) still has localStorage, so the SPA works; only
    // the header-less sub-resource requests lose the session.
    console.warn("Failed to sync the session cookie:", e);
  }
};

export const isTokenExpired = (bufferMs: number = REQUEST_TOKEN_EXPIRY_BUFFER_MS): boolean => {
  if (!tokenExpiresAt) return true;
  // Consider expired with a safety buffer before actual expiry.
  return new Date() >= new Date(tokenExpiresAt.getTime() - bufferMs);
};

// Returns true if a token exists in localStorage, even if it is expired.
// Used to decide whether to attempt GetCurrentUser on app init — if no token
// was ever stored, the user is definitively not logged in and there is nothing
// to refresh, so we can skip the network round-trip entirely.
export const hasStoredToken = (): boolean => {
  if (accessToken) return true;
  try {
    return !!localStorage.getItem(TOKEN_KEY);
  } catch {
    return false;
  }
};

export const clearAccessToken = (): void => {
  accessToken = null;
  tokenExpiresAt = null;

  try {
    localStorage.removeItem(TOKEN_KEY);
    localStorage.removeItem(EXPIRES_KEY);
  } catch (e) {
    console.warn("Failed to clear localStorage:", e);
  }
  // The cookie is a copy of the token, so it has to go with it. A
  // leftover cookie would keep serving file reads for a session the
  // user has just signed out of — and since the server resolves it to
  // a user record, it would look like a live session until the token
  // itself expired.
  syncAuthCookie(null);
};
