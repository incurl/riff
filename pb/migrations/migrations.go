// Package migrations holds user-defined Go migrations for riff.
//
// Each migration file in this package registers an Up and Down function
// with the userland migrations.Register helper from PocketBase, which
// in turn appends to core.AppMigrations. The migratecmd plugin and
// app.Bootstrap() then pick them up automatically.
//
// Migration files are named <unix-timestamp>_<purpose>.go and must
// each call migrations.Register() in an init() function.
package migrations
