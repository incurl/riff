package connect

// Legacy upload-stub detection.
//
// Early riff builds required `UploadAttachment.spec.attachment.memo`,
// so the upstream-Memos V0 (Memos 0.21) upload path — which has no memo
// to name yet — manufactured a hidden **stub memo** and uploaded against
// it. The stub was a phantom `memos` row per upload that the follow-up
// PATCH had to clean up, and it could only clean up a stub whose every
// file moved. A client that uploaded twice and bound one file left the
// stub behind holding the other, which is how "my post became two memos,
// one empty with the file on it" kept recurring.
//
// The requirement is gone. `attachment.memo` is now optional and an
// upload with no memo lands as an *unlinked attachment* — upstream's own
// model (`Attachment.memo` is `optional`, and `GetAttachment` documents
// "an unlinked attachment is visible only to its uploader and admins").
// See `pb/connect/memo_bind_attachments.go` for the bind pass.
//
// The sentinel is retained purely to recognise rows written by those
// earlier builds, which are still present in live databases. Two things
// depend on it: the list handlers filter stubs out of the feed, and
// `pb/migrations/1900000000_sweep_v0_upload_stubs.go` deletes them.
//
// Do not write new stubs. An upload belongs to whoever uploaded it until a
// memo claims it.

// StubMemoContent is the sentinel content every legacy V0 upload stub memo
// carries.
//
// It is a zero-width space (U+200B): invisible in a
// renderer, and never produced by a real memo.
const StubMemoContent = "\u200b"

// IsStubMemoContent reports whether a memo's content is the
// legacy stub sentinel.
//
// This is the single test for "is this a legacy stub", used by the V0 list
// shaper, the V1 list handler, and the cleanup migration. The memo's
// visibility is deliberately not part of the test: a stub that a later V0
// edit flipped to PUBLIC is still a stub and still must not be shown.
func IsStubMemoContent(content string) bool {
	return content == StubMemoContent
}
