import { describe, expect, it } from "vitest";
import type { Attachment } from "@/types/proto/api/v1/attachment_service_pb";
import {
  getAttachmentMotionClipUrl,
  getAttachmentMotionClipUrlWith,
  getAttachmentThumbnailUrl,
  getAttachmentThumbnailUrlWith,
  getAttachmentUrl,
  getAttachmentUrlWith,
} from "@/utils/attachment";

const origin = window.location.origin;

const baseAttachment = {
  name: "attachments/test-uid",
  filename: "photo.png",
  type: "image/png",
} as Attachment;

// Regression tests for #6128: share-mode thumbnails/motion clips must keep the share token on externalLink.
describe("attachment URL builders in share mode", () => {
  it("appends thumbnail=true to an externalLink that carries a share token", () => {
    const attachment = {
      ...baseAttachment,
      externalLink: `${origin}/file/attachments/test-uid/photo.png?share_token=abc123`,
    } as Attachment;

    const url = new URL(getAttachmentThumbnailUrl(attachment));
    expect(url.searchParams.get("thumbnail")).toBe("true");
    expect(url.searchParams.get("share_token")).toBe("abc123");
  });

  it("appends motion=true to an externalLink that carries a share token", () => {
    const attachment = {
      ...baseAttachment,
      externalLink: `${origin}/file/attachments/test-uid/photo.png?share_token=abc123`,
    } as Attachment;

    const url = new URL(getAttachmentMotionClipUrl(attachment));
    expect(url.searchParams.get("motion")).toBe("true");
    expect(url.searchParams.get("share_token")).toBe("abc123");
  });

  it("keeps the server thumbnail URL when externalLink has no share token", () => {
    const attachment = {
      ...baseAttachment,
      externalLink: "https://cdn.example.com/photo.png?version=xyz",
    } as Attachment;

    expect(getAttachmentThumbnailUrl(attachment)).toBe(`${origin}/file/attachments/test-uid/photo.png?thumbnail=true`);
  });

  it("keeps the server thumbnail URL when no externalLink is set", () => {
    expect(getAttachmentThumbnailUrl(baseAttachment)).toBe(`${origin}/file/attachments/test-uid/photo.png?thumbnail=true`);
  });

  it("leaves getAttachmentUrl returning the externalLink verbatim", () => {
    const attachment = {
      ...baseAttachment,
      externalLink: `${origin}/file/attachments/test-uid/photo.png?share_token=abc123`,
    } as Attachment;

    expect(getAttachmentUrl(attachment)).toBe(attachment.externalLink);
  });
});

// Pure-form coverage: the SPA's `${BASE_URL}file/...` shape must be
// sub-path-aware. Without these, the SPA would emit
// `<origin>/file/...` on a sub-path mount and the reverse proxy would
// 308-redirect every image fetch through its SPA fallback.
describe("attachment URL builders with explicit baseUrl", () => {
  it("getAttachmentUrlWith builds a sub-path-aware server URL", () => {
    expect(getAttachmentUrlWith(`${origin}/community/`, baseAttachment)).toBe(`${origin}/community/file/attachments/test-uid/photo.png`);
  });

  it("getAttachmentThumbnailUrlWith builds a sub-path-aware server URL with the thumbnail flag", () => {
    expect(getAttachmentThumbnailUrlWith(`${origin}/community/`, baseAttachment)).toBe(
      `${origin}/community/file/attachments/test-uid/photo.png?thumbnail=true`,
    );
  });

  it("getAttachmentMotionClipUrlWith builds a sub-path-aware server URL with the motion flag", () => {
    expect(getAttachmentMotionClipUrlWith(`${origin}/community/`, baseAttachment)).toBe(
      `${origin}/community/file/attachments/test-uid/photo.png?motion=true`,
    );
  });

  it("a root-mount base URL still produces a leading slash so the path is well-formed", () => {
    // The trailing slash on the baseUrl is load-bearing: without it the
    // concatenation collapses to `<origin>file/...` and the URL is wrong
    // in a way the browser's URL parser would silently paper over.
    expect(getAttachmentUrlWith(`${origin}/`, baseAttachment)).toBe(`${origin}/file/attachments/test-uid/photo.png`);
  });

  it("externalLink still wins over any baseUrl", () => {
    const external = `${origin}/community/file/attachments/test-uid/photo.png?share_token=abc`;
    const attachment = { ...baseAttachment, externalLink: external } as Attachment;
    expect(getAttachmentUrlWith(`${origin}/community/`, attachment)).toBe(external);
  });
});
