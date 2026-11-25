// The admin permission scope's pure half.
//
// The scope is what makes a RESTRICTED tag restrict: a tag that inherits
// the global grant restricts nobody, and the member group holds
// `startDiscussion` globally.
//
// So the two things pinned here are the two an operator's click depends
// on:
//
//   - the KEY the dropdown writes. A bare permission is a GLOBAL grant;
//     only the scoped key reaches the row that governs the tag.
//   - the ID inside it. This port's tag ids are PocketBase ids (15
//     characters of digits and letters), not upstream's integers, so a
//     pattern that demanded a numeric id would match nothing — and the
//     grid would silently write a global grant.

const { SCOPED_PERMISSIONS, globalPermission, scopedKey, scopedRequired } = require('./scope');

describe('scopedKey', () => {
  it('builds upstream spelling from a PocketBase tag id', () => {
    expect(scopedKey('abc123def456ghi', 'viewForum')).toBe('tagabc123def456ghi.viewForum');
  });

  it('builds the same key upstream builds for an integer id', () => {
    // Upstream's ids are integers, and its frontend writes `tag5.viewForum`.
    // Both spellings have to work, because the server splits the key.
    expect(scopedKey('5', 'startDiscussion')).toBe('tag5.startDiscussion');
  });
});

describe('globalPermission', () => {
  it('strips the scope from a PocketBase-id key', () => {
    expect(globalPermission('tagabc123def456ghi.viewForum')).toBe('viewForum');
  });

  it('strips the scope from an integer-id key', () => {
    expect(globalPermission('tag5.startDiscussion')).toBe('startDiscussion');
  });

  it('keeps a scoped discussion ability scoped-aware', () => {
    // `discussion.reply` contains a dot of its own: the scope is the
    // LEADING `tag{id}.`, and the rest is the permission.
    expect(globalPermission('tagabc123def456ghi.discussion.reply')).toBe('discussion.reply');
  });

  it('leaves an unscoped permission alone', () => {
    expect(globalPermission('discussion.reply')).toBe('discussion.reply');
  });

  it('leaves a permission whose name merely starts with "tag" alone', () => {
    // No dot after an id, so there is no scope to strip.
    expect(globalPermission('tagBypassTagCounts')).toBe('tagBypassTagCounts');
  });

  it('tolerates a missing permission', () => {
    expect(globalPermission(undefined)).toBeUndefined();
    expect(globalPermission(null)).toBeNull();
  });
});

describe('scopedRequired', () => {
  it('carries the scope back onto every required permission', () => {
    expect(scopedRequired(['viewForum'], 'tagabc123def456ghi.startDiscussion')).toEqual(['tagabc123def456ghi.viewForum']);
  });

  it('returns the requirements untouched for an unscoped key', () => {
    expect(scopedRequired(['viewForum'], 'discussion.reply')).toEqual(['viewForum']);
  });

  it('handles an empty requirement list', () => {
    expect(scopedRequired([], 'tag5.startDiscussion')).toEqual([]);
  });
});

describe('SCOPED_PERMISSIONS', () => {
  it("is upstream's two forum-wide permissions", () => {
    // Upstream's `addTagsPermissionScope` renders the scoped dropdown for
    // `viewForum`, `startDiscussion`, and the `discussion.*` abilities.
    expect(SCOPED_PERMISSIONS).toEqual(['viewForum', 'startDiscussion']);
  });
});
