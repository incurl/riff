package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// createPo33SketchDownloadsCollection is the
// device-side audit log for every successful sketch
// download a bound device makes. One row per
// (device, memo) tuple per fetch — the same device can
// re-fetch the same memo and a new row is written, so
// "I tried to download a sketch at 03:00 and it worked"
// is reproducible from this table alone.
//
// Web-side playback and download do NOT write here;
// the device is the only actor that hits
// /api/po33/sketches/download. Anonymous web playback
// reuses the standard PocketBase file-serve routes and
// is not audited at the device level.
//
// Fields:
//
//	device            (relation → po33_devices, single,
//	                    required, indexed) — the
//	                    bound device that pulled the
//	                    sketch.
//	memo              (relation → memos, single,
//	                    required, indexed) — the sketch
//	                    memo the device fetched.
//	attachment_kind   (select, required;
//	                    values: PATCH, SAMPLE) — which
//	                    side of the memo the device
//	                    pulled. A single sketch upload
//	                    creates one `patches` and one
//	                    `samples` row, and the device
//	                    pulls each side separately.
//	remote_addr       (text, optional, max 64) — the
//	                    client IP at the time of the
//	                    fetch. Captured here rather than
//	                    relying on a separate
//	                    access-log file so a security
//	                    review has a single source of
//	                    truth.
//	created           (autodate, onCreate)
//
// Access rules: every verb is nil. The Connect handler
// is the sole authority. The collection has no
// viewable REST surface; even an instance admin reads
// it through the migration's own tool, not the
// collection API.
func init() {
	migrations.Register(func(txApp core.App) error {
		devices, err := txApp.FindCollectionByNameOrId("po33_devices")
		if err != nil {
			return err
		}
		memos, err := txApp.FindCollectionByNameOrId("memos")
		if err != nil {
			return err
		}

		c := core.NewBaseCollection("po33_sketch_downloads")
		c.Fields.Add(&core.RelationField{
			Name:          "device",
			CollectionId:  devices.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: true, // a device deletion purges its download log
		})
		c.Fields.Add(&core.RelationField{
			Name:          "memo",
			CollectionId:  memos.Id,
			Required:      true,
			MaxSelect:     1,
			MinSelect:     1,
			CascadeDelete: false, // a memo deletion does NOT cascade; the audit row stays so the operator can see what the device pulled before the memo was removed
		})
		c.Fields.Add(&core.SelectField{
			Name:      "attachment_kind",
			Required:  true,
			Values:    []string{"PATCH", "SAMPLE"},
			MaxSelect: 1,
		})
		c.Fields.Add(&core.TextField{
			Name: "remote_addr",
			Max:  64,
		})
		c.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})

		c.AddIndex("idx_po33_sketch_downloads_device", false, "device, created", "")
		c.AddIndex("idx_po33_sketch_downloads_memo", false, "memo, created", "")

		c.ListRule = nil
		c.ViewRule = nil
		c.CreateRule = nil
		c.UpdateRule = nil
		c.DeleteRule = nil

		return txApp.Save(c)
	}, func(txApp core.App) error {
		c, err := txApp.FindCollectionByNameOrId("po33_sketch_downloads")
		if err != nil {
			return err
		}
		return txApp.Delete(c)
	})
}
