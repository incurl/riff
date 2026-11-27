#!/usr/bin/env node
//
// The headless-browser smoke for the forum and the wiki.
//
// WHY THIS EXISTS. Every serious defect in the forum port's recent history was
// invisible to both the Go suite and the frontend's Jest suite, because the
// Flarum SPA reports *nothing* when the backend does not meet it:
//
//   - an attribute it reads comes back `undefined`, so the control does not
//     render (the tag picker had no confirm button; the tag modal's parent
//     select was hidden; `isLiked` was never registered);
//   - a route it calls does not exist, and the SPA catch-all answers the shell
//     with a 200, which the store parses and turns into `null`;
//   - a lazy core chunk is never fetched, so `this.sortable` is undefined and
//     the drag handler returns early — with a clean console and no failed request.
//
// So this drives a real browser against a real instance, through the four flows
// that have actually broken. It is not a tour of the app.
//
// USAGE
//
//   node scripts/smoke/forum_smoke.mjs            # boots its own throwaway instance
//   RIFF_SMOKE_BASE_URL=http://host:port \
//   RIFF_SMOKE_ADMIN_EMAIL=… RIFF_SMOKE_ADMIN_PASSWORD=… \
//     node scripts/smoke/forum_smoke.mjs          # uses a running instance
//
// Environment: RIFF_SMOKE_BASE_URL, RIFF_SMOKE_ADMIN_EMAIL / _PASSWORD (required
// with BASE_URL), RIFF_SMOKE_BINARY (default ./riff), RIFF_SMOKE_DATA_DIR,
// RIFF_SMOKE_PORT (default 18099), RIFF_SMOKE_TIMEOUT (ms per step, default
// 15000), PLAYWRIGHT_CHROMIUM_EXECUTABLE, RIFF_SMOKE_HEADLESS=0.
//
// Exit code 0 only when every flow passed.

import { createRequire } from 'node:module';
import { spawn } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import process from 'node:process';

const require = createRequire(import.meta.url);

const TIMEOUT = Number(process.env.RIFF_SMOKE_TIMEOUT || 15000);
const HEADLESS = process.env.RIFF_SMOKE_HEADLESS !== '0';
const RUN_ID = Date.now().toString(36);
const BINARY = process.env.RIFF_SMOKE_BINARY || './riff';
const PORT = process.env.RIFF_SMOKE_PORT || '18099';
const DEBUG = process.env.RIFF_SMOKE_DEBUG === '1';
const ONLY = process.env.RIFF_SMOKE_ONLY || '';

/** The instance every helper talks to. Set once in `main`. */
let baseUrl = '';

/** One entry per flow: `{name, ok, error?}`. */
const results = [];

/**
 * Load playwright.
 *
 * `playwright` is what this directory's package.json installs; `playwright-core`
 * is accepted too, because a developer machine may only have a browser build and
 * the core package. `PLAYWRIGHT_CHROMIUM_EXECUTABLE` names the browser itself.
 */
function loadPlaywright() {
  // An explicit path (or package name) wins: a machine may carry a cached
  // chromium and a playwright-core under an unrelated node_modules.
  if (process.env.RIFF_SMOKE_PLAYWRIGHT) return require(process.env.RIFF_SMOKE_PLAYWRIGHT);

  for (const id of ['playwright', 'playwright-core']) {
    try {
      return require(id);
    } catch {
      // try the next one
    }
  }

  throw new Error(
    'playwright is not installed: run `npm install` in scripts/smoke and ' +
      '`npx playwright install --with-deps chromium`, or set PLAYWRIGHT_CHROMIUM_EXECUTABLE.'
  );
}

/** Log a line with the smoke's prefix. */
function log(message) {
  process.stdout.write(`[smoke] ${message}\n`);
}

/** A unique suffix, so repeated runs against one instance do not collide. */
function unique(base) {
  return `${base} ${RUN_ID}`;
}

/** Sleep. */
function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/**
 * A JSON fetch helper.
 *
 * The body is read as TEXT first: an unrouted path answers the SPA shell with a
 * 200 and an HTML body, and `JSON.parse` on that has to fail with the status and
 * the start of the body, not with `Unexpected token <`.
 */
async function api(pathname, { method = 'GET', token, body } = {}) {
  const response = await fetch(`${baseUrl}${pathname}`, {
    method,
    headers: {
      ...(body ? { 'Content-Type': 'application/json' } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });

  const text = await response.text();

  let json = null;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    throw new Error(`${method} ${pathname} answered ${response.status} with a non-JSON body: ${text.slice(0, 200)}`);
  }

  if (!response.ok) {
    throw new Error(`${method} ${pathname} answered ${response.status}: ${text.slice(0, 400)}`);
  }

  return json;
}

/**
 * Boot a throwaway instance, and return it with a way to stop it.
 *
 * The FIRST superuser is created through PocketBase's own installer token: a
 * fresh instance prints `…/_/#/pbinstall/<token>` to stdout, and that token is a
 * superuser auth token for 30 minutes, so it can create a real account over
 * HTTP. That keeps this script self-contained — no Go seeder, no second boot.
 */
async function bootInstance() {
  const dataDir = process.env.RIFF_SMOKE_DATA_DIR || (await mkdtemp(path.join(tmpdir(), 'riff-smoke-')));
  const owned = !process.env.RIFF_SMOKE_DATA_DIR;

  // A port already in use is not a slow boot: the health probe would answer from
  // the OTHER instance, and the run would then fail somewhere confusing (a
  // superuser created on one instance, authenticated against another).
  try {
    const inUse = await fetch(`http://127.0.0.1:${PORT}/api/health`);
    if (inUse.ok) throw new Error(`port ${PORT} is already serving an instance; set RIFF_SMOKE_PORT`);
  } catch (error) {
    if (!String(error).includes('already serving')) {
      // nothing listening: expected
    } else {
      throw error;
    }
  }

  log(`booting ${BINARY} serve --dir ${dataDir} --http 127.0.0.1:${PORT}`);

  const child = spawn(BINARY, ['serve', '--dir', dataDir, '--http', `127.0.0.1:${PORT}`], {
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  let output = '';
  const collect = (chunk) => {
    output += chunk.toString();
  };

  child.stdout.on('data', collect);
  child.stderr.on('data', collect);

  const deadline = Date.now() + TIMEOUT * 4;
  let installToken = null;
  let healthy = false;

  while (Date.now() < deadline) {
    const match = /pbinstall\/([A-Za-z0-9._-]+)/.exec(output);
    if (match) installToken = match[1];

    if (!healthy) {
      try {
        healthy = (await fetch(`http://127.0.0.1:${PORT}/api/health`)).ok;
      } catch {
        // not listening yet
      }
    }

    if (healthy && installToken) break;

    await sleep(250);
  }

  if (!healthy) {
    child.kill('SIGKILL');
    throw new Error(`the instance did not become healthy. Output:\n${output.slice(-2000)}`);
  }

  return {
    installToken,
    output: () => output,
    async stop() {
      child.kill('SIGTERM');
      await sleep(500);
      if (child.exitCode === null) child.kill('SIGKILL');
      if (owned) await rm(dataDir, { recursive: true, force: true });
    },
  };
}

/**
 * Create the administrator the flows sign in as, and return its auth token.
 *
 * With RIFF_SMOKE_BASE_URL the administrator is the caller's, and is only
 * authenticated.
 */
async function authenticate(instance) {
  const email = process.env.RIFF_SMOKE_ADMIN_EMAIL || `smoke-${RUN_ID}@example.com`;
  const password = process.env.RIFF_SMOKE_ADMIN_PASSWORD || `smoke-${RUN_ID}-passw0rd`;

  if (instance?.installToken) {
    await api('/api/collections/_superusers/records', {
      method: 'POST',
      token: instance.installToken,
      body: { email, password, passwordConfirm: password },
    });
  }

  const session = await api('/api/collections/_superusers/auth-with-password', {
    method: 'POST',
    body: { identity: email, password },
  });

  return { email, password, token: session.token };
}

/**
 * Create a MEMBER account and sign it in.
 *
 * The authoring flows run as a member, because that is who authoring is for: the
 * author columns are relations to the shared `users` collection, so a PocketBase
 * superuser has no forum profile to stamp (the server refuses with a 403 saying
 * exactly that). Using a member also exercises the permission path a real
 * reader takes rather than an administrator's bypass.
 */
async function createMember() {
  const email = `smoke-member-${RUN_ID}@example.com`;
  const password = `smoke-${RUN_ID}-passw0rd`;

  await api('/api/collections/users/records', {
    method: 'POST',
    // The host's `users.username` is 3-30 chars of `[a-z0-9_]`, so the run id's
    // base36 characters are joined with an underscore, never a hyphen.
    body: { email, password, passwordConfirm: password, username: `smoke_${RUN_ID}` },
  });

  const session = await api('/api/collections/users/auth-with-password', {
    method: 'POST',
    body: { identity: email, password },
  });

  return { email, token: session.token };
}

/** Create a tag through the forum API, and return its resource. */
async function createTag(auth, attributes) {
  const document = await api('/forum/api/tags', {
    method: 'POST',
    token: auth.token,
    body: { data: { type: 'tags', attributes } },
  });

  return document.data;
}

/** Read every tag the forum serves, flattened to what the assertions need. */
async function listTags(auth) {
  const document = await api('/forum/api/tags', { token: auth.token });

  return document.data.map((resource) => ({
    id: resource.id,
    name: resource.attributes.name,
    position: resource.attributes.position,
    isChild: resource.attributes.isChild,
    parent: resource.relationships?.parent?.data?.id ?? null,
  }));
}

/** Find a tag by name, failing with the list when it is absent. */
function tagNamed(tags, name) {
  const tag = tags.find((candidate) => candidate.name === name);

  if (!tag) throw new Error(`the tag "${name}" is not listed: ${tags.map((t) => t.name).join(', ')}`);

  return tag;
}

/**
 * The flows: composer, drag, parent select, branding.
 *
 * Each receives the page (signed in by cookie) and the auth token, and throws
 * with a message that says what a reader would have seen.
 */
const flows = [
  {
    name: 'composer: a chosen tag reaches the discussion',
    as: 'member',
    async run({ page, admin }) {
      const tagName = unique('Smoke Tag');

      // The tag is SEEDED by the administrator (a member may not create tags) and
      // CHOSEN by the member, which is the ordinary author's path.
      await createTag(admin, { name: tagName, slug: `smoke-tag-${RUN_ID}`, isPrimary: true });

      await page.goto(`${baseUrl}/forum/`, { waitUntil: 'networkidle' });
      await page.locator('.IndexPage-newDiscussion').first().click();

      await page.locator('.DiscussionComposer-title').fill(unique('Smoke discussion'));
      await page.locator('.Composer textarea').first().fill('Posted by the forum smoke.');

      // THE CONFIRM CONTROL IS THE POINT. The picker once had none, so its only
      // exits were the close button and the backdrop — neither calls `onsubmit`
      // — and every selection was discarded.
      await page.locator('.DiscussionComposer-changeTags').first().click();

      const item = page.locator('.TagSelectionModal-button', { hasText: tagName }).first();
      await item.waitFor({ timeout: TIMEOUT });
      await item.click();

      const confirm = page.locator('.TagSelectionModal-actions .Button--primary').first();
      await confirm.waitFor({ timeout: TIMEOUT });

      // The button is disabled until the selection meets the count rules, and the
      // enable happens on the redraw after the click: WAIT for it rather than
      // reading a stale attribute. A timeout here means the rules are wrong, not
      // that the click was lost.
      await page
        .waitForFunction(
          () => {
            const button = document.querySelector('.TagSelectionModal-actions .Button--primary');
            return !!button && !button.disabled;
          },
          undefined,
          { timeout: TIMEOUT }
        )
        .catch(() => {
          throw new Error('the picker never lets a selection that meets the count rules be confirmed');
        });

      await confirm.click();

      // `m.redraw()` in the modal's callback is asynchronous: wait for the header
      // to carry the tag rather than reading it once and racing the redraw.
      await page
        .locator('.DiscussionComposer-changeTags', { hasText: tagName })
        .first()
        .waitFor({ timeout: TIMEOUT })
        .catch(async () => {
          const label = (await page.locator('.DiscussionComposer-changeTags').first().textContent()) || '';
          throw new Error(`the composer did not keep the chosen tag (its label reads "${label}")`);
        });

      await page.locator('.Composer .Button--primary').first().click();
      await page.waitForURL(/\/forum\/d\//, { timeout: TIMEOUT });

      // And the discussion SHOWS it. This is the half an `included` resource with
      // an empty `tags` relationship used to erase: the tag was saved, every
      // payload carried it, and the page rendered an untagged discussion.
      await page.locator('.DiscussionHero .TagsLabel-item', { hasText: tagName }).first().waitFor({ timeout: TIMEOUT });
    },
  },
  {
    name: 'admin: dragging a primary tag reorders and persists',
    async run({ page, auth }) {
      const first = unique('Smoke Primary A');
      const second = unique('Smoke Primary B');

      await createTag(auth, { name: first, slug: `smoke-a-${RUN_ID}`, isPrimary: true });
      await createTag(auth, { name: second, slug: `smoke-b-${RUN_ID}`, isPrimary: true });

      await page.goto(`${baseUrl}/forum/admin#/extension/riff-tags`, { waitUntil: 'networkidle' });

      const items = page.locator('.TagList--primary > li');
      const secondItem = items.filter({ hasText: second }).first();
      const firstItem = items.filter({ hasText: first }).first();
      await secondItem.waitFor({ timeout: TIMEOUT });

      const from = await secondItem.boundingBox();
      const to = await firstItem.boundingBox();
      if (!from || !to) throw new Error('the tag list did not render boxes to drag');

      const orderResponse = page.waitForResponse((response) => response.url().includes('/tags/order'), {
        timeout: TIMEOUT,
      });

      // A REAL mouse drag: sortable listens for mousedown/mousemove/mouseup, so a
      // synthetic HTML5 drag never reaches it. This is also the flow that proves
      // the lazily chunked library was actually loaded: without it the list is
      // inert and no request is made at all.
      const x = from.x + from.width / 2;
      await page.mouse.move(x, from.y + from.height / 2);
      await page.mouse.down();
      await sleep(120);
      await page.mouse.move(x, from.y + from.height / 2 - 10, { steps: 5 });
      await page.mouse.move(x, to.y + 4, { steps: 12 });
      await sleep(200);
      await page.mouse.up();

      const response = await orderResponse;
      if (response.status() !== 204) throw new Error(`the reorder should answer 204, got ${response.status()}`);

      await sleep(600);

      const tags = await listTags(auth);
      const a = tagNamed(tags, first);
      const b = tagNamed(tags, second);
      if (!(a.position !== null && b.position !== null && b.position < a.position)) {
        throw new Error(`the drag did not persist: ${first}=${a.position}, ${second}=${b.position}`);
      }

      // And the tree keeps it, rather than snapping back: the server answers 204,
      // so a redraw without the local store update renders the LOADED order.
      const names = await page.locator('.TagList--primary > li .TagListItem-name').allTextContents();
      if (names.indexOf(second) > names.indexOf(first)) {
        throw new Error(`the tree snapped back to the old order: ${names.join(' | ')}`);
      }
    },
  },
  {
    name: 'admin: the tag modal offers a parent, and a child lands in the tree',
    async run({ page, auth }) {
      const primaryName = unique('Smoke Parent');
      const childName = unique('Smoke Child');

      const primary = await createTag(auth, {
        name: primaryName,
        slug: `smoke-parent-${RUN_ID}`,
        isPrimary: true,
      });

      await page.goto(`${baseUrl}/forum/admin#/extension/riff-tags`, { waitUntil: 'networkidle' });

      const primaryItem = page.locator('.TagList--primary > li').filter({ hasText: primaryName }).first();
      await primaryItem.waitFor({ timeout: TIMEOUT });

      // The "+" on a primary tag opens the modal with the parent preselected,
      // and it is VISIBLE without hovering: a hover-only affordance is how "the
      // UI does not accommodate secondary tag creation" looked in the first
      // place, and this control exists to answer it.
      const addChild = primaryItem.locator('.TagListItem-addChild');
      if (!(await addChild.isVisible())) throw new Error('the "add secondary tag" control is not visible until hover');
      await addChild.click();

      // THE PRESENT-BUT-HIDDEN CHECK: the select has to exist for an existing
      // tag. Hiding it on a root left the operator with a drag and nothing else,
      // and "I do not see the parent tag select" looked exactly like a missing
      // feature.
      await page.locator('.Modal-body label', { hasText: 'Parent tag' }).first().waitFor({ timeout: TIMEOUT });

      await page.locator('input[placeholder="Tag name"]').fill(childName);
      await page.locator('.EditTagModal-save').click();

      await page.locator('.Modal').waitFor({ state: 'detached', timeout: TIMEOUT }).catch(() => {});

      // The tree renders children by FILTERING THE STORE on `parent()`, so a
      // child that is not nested here is a child that resolves to no parent.
      await page.locator('.TagListItem-children > li', { hasText: childName }).first().waitFor({ timeout: TIMEOUT });

      const child = tagNamed(await listTags(auth), childName);
      if (!child.isChild || child.parent !== primary.id) {
        throw new Error(`the child is not under its primary: isChild=${child.isChild} parent=${child.parent}`);
      }
    },
  },
  {
    name: 'wiki: a renamed site reaches the served shell',
    async run({ auth }) {
      const before = await api('/wiki/api/branding');
      const siteName = unique('Smoke Wiki');

      const updated = await api('/wiki/api/branding', {
        method: 'PUT',
        token: auth.token,
        body: { siteName },
      });

      // A 200 WITH NO BODY is the failure that produced
      // "Cannot read properties of null (reading 'faviconFile')" in the SPA: its
      // transport resolves an ok response with no JSON to `null`.
      if (!updated || updated.siteName !== siteName) {
        throw new Error(`the branding write did not answer the configuration: ${JSON.stringify(updated)}`);
      }

      const shell = await (await fetch(`${baseUrl}/wiki/`)).text();
      if (!shell.includes(siteName)) {
        const title = /<title>([^<]*)/.exec(shell)?.[1] ?? '?';
        throw new Error(`the served shell does not carry the new site name (its title reads "${title}")`);
      }

      // Leave the instance as it was found, so a rerun is clean.
      await api('/wiki/api/branding', { method: 'PUT', token: auth.token, body: { siteName: before.siteName } });
    },
  },
];

/**
 * A browser context signed in as the administrator.
 *
 * The forum reads PocketBase's own token from a cookie (`forum_access_token`),
 * and `forum_csrf` is the value it echoes in `X-CSRF-Token`.
 */
async function signedInContext(browser, auth) {
  const hostname = new URL(baseUrl).hostname;
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });

  await context.addCookies([
    { name: 'forum_access_token', value: auth.token, domain: hostname, path: '/' },
    { name: 'forum_csrf', value: `smoke-${RUN_ID}`, domain: hostname, path: '/' },
  ]);

  const page = await context.newPage();

  // A page error is always a defect: reading a field off `null` (the SPA's
  // transport for an `ok` response with no JSON) is how half of these bugs
  // presented, and it never reaches the network log. The list lives here rather
  // than on the Page object, which is a proxy that may not take properties.
  if (DEBUG) {
    page.on('console', (message) => process.stdout.write(`[smoke]   console.${message.type()}: ${message.text().slice(0, 300)}\n`));
    page.on('requestfailed', (request) => process.stdout.write(`[smoke]   request failed: ${request.method()} ${request.url()}\n`));
    page.on('response', async (response) => {
      if (response.status() >= 400) {
        process.stdout.write(
          `[smoke]   ${response.status()} ${response.request().method()} ${response.url()} — ${(await response.text().catch(() => '')).slice(0, 300)}\n`
        );
      }
    });
  }

  const errors = [];
  page.on('pageerror', (error) => {
    const message = error.message.split('\n')[0];
    process.stdout.write(`[smoke]   page error: ${message}\n`);
    errors.push(message);
  });

  return { context, page, errors };
}

async function main() {
  let instance = null;
  let browser = null;

  try {
    if (!process.env.RIFF_SMOKE_BASE_URL) {
      instance = await bootInstance();
      process.env.RIFF_SMOKE_BASE_URL = `http://127.0.0.1:${PORT}`;
    }

    baseUrl = process.env.RIFF_SMOKE_BASE_URL.replace(/\/+$/, '');

    const auth = await authenticate(instance);
    const member = await createMember();
    log(`signed in as ${auth.email} (administrator) and ${member.email} (member) on ${baseUrl}`);

    const { chromium } = loadPlaywright();
    browser = await chromium.launch({
      headless: HEADLESS,
      executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || undefined,
    });

    for (const flow of flows) {
      if (ONLY && !flow.name.includes(ONLY)) continue;

      const identity = flow.as === 'member' ? member : auth;
      const { context, page, errors } = await signedInContext(browser, identity);

      try {
        await flow.run({ page, auth: identity, admin: auth });

        if (errors.length) throw new Error(`the page raised an uncaught error: ${errors[0]}`);

        results.push({ name: flow.name, ok: true });
        log(`PASS  ${flow.name}`);
      } catch (error) {
        results.push({ name: flow.name, ok: false, error: error.message });
        log(`FAIL  ${flow.name}\n        ${error.message.split('\n').join('\n        ')}`);
      } finally {
        await context.close();
      }
    }
  } finally {
    if (browser) await browser.close();
    if (instance) await instance.stop();
  }

  const failed = results.filter((result) => !result.ok);

  // A failing flow is usually the backend's answer, so show what the instance
  // itself said. In CI this is the only place that output exists.
  if (failed.length && instance?.output) {
    log('');
    log('the instance said:');
    for (const line of instance.output().split('\n').slice(-40)) log(`  ${line}`);
  }

  log('');
  log(`${results.length - failed.length}/${results.length} flows passed`);

  process.exit(failed.length === 0 ? 0 : 1);
}

main().catch((error) => {
  log(`the smoke could not run: ${error.message}`);
  process.exit(1);
});
