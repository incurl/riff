// Package storage provides pluggable backends for the file attachments
// stored in the `patches` and `samples` PocketBase collections.
//
// The default backend is [Local] — PocketBase writes file bytes to its
// `pb_data/storage/` directory, and the existing `patches`/`samples`
// `FileField` definitions serve them. Operators can switch to an
// S3-compatible backend by setting
// `InstanceSetting.StorageSetting.s3_config` on the instance; the
// `pb/hooks/storage_bridge.go` hook then mirrors each new or replaced
// attachment to the remote bucket and records the public URL in the
// collection's `remote_url` column.
//
// The `Storage` interface is intentionally small — Put, Get, Delete,
// URL, Stat — so that future backends (Cloudflare R2, GCS, Azure Blob)
// can implement it without depending on riff's `pb/` layer. The package
// is `internal` because it ships with riff and is not intended to be
// imported by third-party modules; the public surface is the Memos-
// shaped `InstanceSetting.Storage` proto, which is bound to this
// package only by the bridge hook.
package storage
