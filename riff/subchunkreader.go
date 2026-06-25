// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package riff

import (
	"bytes"
	"errors"
	"io"
)

var (
	errInvalidSubChunkHeader = errors.New("riff: could not read sub-chunk header")
	errInvalidSubChunkData   = errors.New("riff: could not read sub-chunk data")
)

// SubChunkReader reads sub-chunks embedded within a RIFF chunk.
// For example, ANMF chunks in animated WebP files contain ALPH, VP8,
// and VP8L sub-chunks.
type SubChunkReader struct {
	r io.Reader
}

// NewSubChunkReader returns a new SubChunkReader that reads sub-chunks from r.
func NewSubChunkReader(r io.Reader) *SubChunkReader {
	return &SubChunkReader{r: r}
}

// Next returns the FourCC, data, and data length of the next sub-chunk.
// The returned io.Reader is backed by a separate buffer and is safe to
// discard without fully reading its contents.
func (c *SubChunkReader) Next() (FourCC, io.Reader, uint32, error) {
	header := make([]byte, chunkHeaderSize)
	n, err := io.ReadFull(c.r, header)
	if err != nil {
		if err == io.ErrUnexpectedEOF {
			return FourCC{}, nil, 0, errInvalidSubChunkHeader
		}
		return FourCC{}, nil, 0, err
	}
	if n != chunkHeaderSize {
		return FourCC{}, nil, 0, errInvalidSubChunkHeader
	}

	fourCC := FourCC{header[0], header[1], header[2], header[3]}
	chunkLen := u32(header[4:8])
	buf := make([]byte, chunkLen)
	n, err = io.ReadFull(c.r, buf)
	if err != nil {
		if err == io.ErrUnexpectedEOF {
			return FourCC{}, nil, 0, errInvalidSubChunkData
		}
		return FourCC{}, nil, 0, err
	}
	if n != int(chunkLen) {
		return FourCC{}, nil, 0, errInvalidSubChunkData
	}

	// Maintain 2-byte alignment per RIFF spec.
	if chunkLen%2 == 1 {
		padding := make([]byte, 1)
		if _, err := io.ReadFull(c.r, padding); err != nil {
			return FourCC{}, nil, 0, err
		}
	}

	return fourCC, bytes.NewReader(buf), chunkLen, nil
}
