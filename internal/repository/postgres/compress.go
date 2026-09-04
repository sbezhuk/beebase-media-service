package postgres

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
)

// decompress reverses the gzip compression the pre-R2 repository applied
// to media_blobs.data when it shrank the payload (see LegacyBlobStore and
// MediaRepository.GetLegacyContent). New content never goes through this
// path - it's stored raw in R2 - so this is purely a read path for
// pre-migration data.
func decompress(data []byte, compressed bool) ([]byte, error) {
	if !compressed {
		return data, nil
	}

	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("postgres: gunzip media content: %w", err)
	}
	defer func() { _ = gr.Close() }()

	out, err := io.ReadAll(gr)
	if err != nil {
		return nil, fmt.Errorf("postgres: gunzip media content: %w", err)
	}

	return out, nil
}
