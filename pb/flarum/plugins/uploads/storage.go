package uploads

import (
	"image"
	// The decoders for the formats the dimensions are read from. A
	// blank import registers them: `image.DecodeConfig` refuses a
	// format no decoder claims.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"

	"github.com/pocketbase/pocketbase/core"
)

// multipartFileReader adapts a multipart part to the reader interface
// PocketBase's `filesystem.File` expects.
//
// The interface asks for `Open()`, because a `filesystem.File` may
// come from a path, a byte slice or a multipart form, and each of
// those can only be opened once. A multipart part is opened per call,
// which is what the framework does with it.
type multipartFileReader struct {
	// header is the multipart part's header.
	header *multipart.FileHeader
}

// Open opens the multipart part.
func (r *multipartFileReader) Open() (io.ReadSeekCloser, error) {
	return r.header.Open()
}

// imageDimensions reads an image's width and height.
//
// The dimensions come from the file itself, and they are what lets the
// frontend lay an image out before it has loaded — upstream's
// `fof/upload` stores the same two columns for the same reason (its
// README calls it "dimension storage (width × height) for JPEG, PNG,
// and GIF — prevents layout shifts when images lazy-load").
//
// A file that is not an image, or whose header cannot be read, simply
// has no dimensions: the caller stores zero and the frontend falls
// back to the unknown-size layout.
func imageDimensions(e *core.RequestEvent, physical, recordID, filename string) (int, int, bool) {
	collection, err := e.App.FindCollectionByNameOrId(physical)
	if err != nil {
		return 0, 0, false
	}

	fsys, err := e.App.NewFilesystem()
	if err != nil {
		return 0, 0, false
	}
	defer func() { _ = fsys.Close() }()

	key := collection.Id + "/" + recordID + "/" + filename

	// Only the header is read: `image.DecodeConfig` stops at the
	// dimensions, so a 20 MB photo is not decoded in full.
	reader, err := fsys.GetReader(key)
	if err != nil {
		return 0, 0, false
	}
	defer func() { _ = reader.Close() }()

	config, _, err := image.DecodeConfig(reader)
	if err != nil {
		return 0, 0, false
	}

	return config.Width, config.Height, true
}
