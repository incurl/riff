import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { AttachmentSchema } from "@/types/proto/api/v1/attachment_service_pb";
import { resolveManagedAttachmentImageSourceWith } from "@/utils/managed-attachment";

// An inline image is stored in the memo's Markdown as a ROOT-RELATIVE
// managed URL (`/file/attachments/<uid>`) — the API only accepts that
// form (see `markdown.ParseManagedAttachmentImageURL`).
//
// The browser resolves a root-relative URL against the document ORIGIN,
// not the SPA's mount point, so on a sub-path mount (`/community/`) it
// misses the riff listener entirely and lands on whatever else the host
// serves at the root.
//
// The attachment gallery never hit this: `getAttachmentUrl` is built
// from `BASE_URL` (origin + mount prefix). The inline image goes
// through `resolveManagedAttachmentImageSource`, whose fallback — the
// case where the memo's attachment list does not carry the row —
// returned the STORED root-relative path unchanged.
//
// The symptom the report describes: the image is fine while the memo is
// open in the editor (the SPA still holds the uploaded blob URL) and
// broken after a refresh, when the memo is re-read from the server and
// the inline row is no longer resolved from the attachment list.

const attachment = create(AttachmentSchema, {
  name: "attachments/inline-uid",
  filename: "photo.png",
  type: "image/png",
});

describe("inline managed images on a sub-path mount", () => {
  it("prefixes the fallback managed path with the mount point", () => {
    // The attachment is NOT in the memo's attachment list, so the
    // resolver falls back to the stored path.
    expect(resolveManagedAttachmentImageSourceWith("/file/attachments/inline-uid", [], "/community")).toBe(
      "/community/file/attachments/inline-uid",
    );
  });

  it("leaves the fallback path alone on a root mount", () => {
    expect(resolveManagedAttachmentImageSourceWith("/file/attachments/inline-uid", [], "/")).toBe("/file/attachments/inline-uid");
  });

  it("still prefers the attachment's own URL when the row is present", () => {
    const withLink = create(AttachmentSchema, {
      ...attachment,
      externalLink: "https://host/file/attachments/inline-uid/photo.png",
    });

    expect(resolveManagedAttachmentImageSourceWith("/file/attachments/inline-uid", [withLink], "/community")).toBe(withLink.externalLink);
  });

  it("leaves an unmanaged image URL untouched", () => {
    expect(resolveManagedAttachmentImageSourceWith("https://example.com/photo.png", [], "/community")).toBe(
      "https://example.com/photo.png",
    );
  });
});
