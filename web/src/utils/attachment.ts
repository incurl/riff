import { Attachment, MotionMediaFamily, MotionMediaRole } from "@/types/proto/api/v1/attachment_service_pb";
import { BASE_URL } from "@/utils/base-path";

// `baseUrl` is the SPA's server-relative origin with a trailing slash
// (see `BASE_URL` in `base-path.ts`). It is parameterised so tests can
// exercise the sub-path branch without overriding module-scope state.
const buildServerFileUrl = (baseUrl: string, attachment: Attachment): string => `${baseUrl}file/${attachment.name}/${attachment.filename}`;

/**
 * Pure-form of {@link getAttachmentUrl} that accepts an explicit server
 * base URL. Exported for unit tests so the sub-path branch can be
 * exercised without overriding the module-scoped `BASE_URL`.
 */
export const getAttachmentUrlWith = (baseUrl: string, attachment: Attachment): string => {
  if (attachment.externalLink) {
    return attachment.externalLink;
  }
  return buildServerFileUrl(baseUrl, attachment);
};

export const getAttachmentUrl = (attachment: Attachment): string => getAttachmentUrlWith(BASE_URL, attachment);

// Appends a thumbnail or motion selector only to share-mode links; other external links fall back to the server-managed attachment URL.
const withShareTokenParam = (externalLink: string | undefined, key: string): string | undefined => {
  if (!externalLink) {
    return undefined;
  }
  const url = new URL(externalLink, window.location.origin);
  if (!url.searchParams.has("share_token")) {
    return undefined;
  }
  url.searchParams.set(key, "true");
  return url.toString();
};

/**
 * Pure-form of {@link getAttachmentThumbnailUrl}; see `getAttachmentUrlWith`
 * for the rationale.
 */
export const getAttachmentThumbnailUrlWith = (baseUrl: string, attachment: Attachment): string => {
  const shareUrl = withShareTokenParam(attachment.externalLink, "thumbnail");
  if (shareUrl) {
    return shareUrl;
  }
  return `${buildServerFileUrl(baseUrl, attachment)}?thumbnail=true`;
};

export const getAttachmentThumbnailUrl = (attachment: Attachment): string => getAttachmentThumbnailUrlWith(BASE_URL, attachment);

/**
 * Pure-form of {@link getAttachmentMotionClipUrl}; see `getAttachmentUrlWith`
 * for the rationale.
 */
export const getAttachmentMotionClipUrlWith = (baseUrl: string, attachment: Attachment): string => {
  const shareUrl = withShareTokenParam(attachment.externalLink, "motion");
  if (shareUrl) {
    return shareUrl;
  }
  return `${buildServerFileUrl(baseUrl, attachment)}?motion=true`;
};

export const getAttachmentMotionClipUrl = (attachment: Attachment): string => getAttachmentMotionClipUrlWith(BASE_URL, attachment);

export const getAttachmentType = (attachment: Attachment) => {
  if (isImage(attachment.type)) {
    return "image/*";
  } else if (attachment.type.startsWith("video")) {
    return "video/*";
  } else if (attachment.type.startsWith("audio") && !isMidiFile(attachment.type)) {
    return "audio/*";
  } else if (attachment.type.startsWith("text")) {
    return "text/*";
  } else if (attachment.type.startsWith("application/epub+zip")) {
    return "application/epub+zip";
  } else if (attachment.type.startsWith("application/pdf")) {
    return "application/pdf";
  } else if (attachment.type.includes("word")) {
    return "application/msword";
  } else if (attachment.type.includes("excel")) {
    return "application/msexcel";
  } else if (attachment.type.startsWith("application/zip")) {
    return "application/zip";
  } else if (attachment.type.startsWith("application/x-java-archive")) {
    return "application/x-java-archive";
  } else {
    return "application/octet-stream";
  }
};

// isImage returns true if the given mime type is an image.
export const isImage = (t: string) => {
  // Don't show PSDs as images.
  return t.startsWith("image/") && !isPSD(t);
};

// isMidiFile returns true if the given mime type is a MIDI file.
export const isMidiFile = (mimeType: string): boolean => {
  return mimeType === "audio/midi" || mimeType === "audio/mid" || mimeType === "audio/x-midi" || mimeType === "application/x-midi";
};

const isPSD = (t: string) => {
  return t === "image/vnd.adobe.photoshop" || t === "image/x-photoshop" || t === "image/photoshop";
};

export const getAttachmentMotionGroupId = (attachment: Attachment): string | undefined => {
  return attachment.motionMedia?.groupId || undefined;
};

export const isAppleLivePhotoStill = (attachment: Attachment): boolean =>
  attachment.motionMedia?.family === MotionMediaFamily.APPLE_LIVE_PHOTO && attachment.motionMedia.role === MotionMediaRole.STILL;

export const isAppleLivePhotoVideo = (attachment: Attachment): boolean =>
  attachment.motionMedia?.family === MotionMediaFamily.APPLE_LIVE_PHOTO && attachment.motionMedia.role === MotionMediaRole.VIDEO;

export const isAndroidMotionContainer = (attachment: Attachment): boolean =>
  attachment.motionMedia?.family === MotionMediaFamily.ANDROID_MOTION_PHOTO &&
  attachment.motionMedia.role === MotionMediaRole.CONTAINER &&
  attachment.motionMedia.hasEmbeddedVideo;

export const isMotionAttachment = (attachment: Attachment): boolean =>
  isAppleLivePhotoStill(attachment) || isAppleLivePhotoVideo(attachment) || isAndroidMotionContainer(attachment);
