// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package webp

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"io"

	"golang.org/x/image/riff"
	"golang.org/x/image/vp8"
	"golang.org/x/image/vp8l"
)

// AnimatedWebP represents a decoded animated WebP image.
type AnimatedWebP struct {
	// Frames contains each frame of the animation.
	Frames []AnimFrame
	// ICCP holds the ICC color profile data, if present.
	ICCP []byte
	// XMP holds XMP metadata, if present.
	XMP []byte
	// EXIF holds EXIF metadata, if present.
	EXIF []byte
	// BackgroundColor is the default background color of the canvas.
	BackgroundColor color.Color
	// LoopCount is the number of times the animation should loop.
	// 0 means infinite looping.
	LoopCount uint16
	// Config holds the canvas dimensions and color model.
	Config image.Config
}

// AnimFrame represents a single frame in an animated WebP image.
type AnimFrame struct {
	// Image is the decoded frame image.
	Image image.Image
	// OffsetX is the horizontal offset of the frame on the canvas.
	OffsetX int
	// OffsetY is the vertical offset of the frame on the canvas.
	OffsetY int
	// Width is the width of the frame.
	Width int
	// Height is the height of the frame.
	Height int
	// Duration is the display duration of the frame in milliseconds.
	Duration int
	// Blend indicates whether the frame should be alpha-blended (true)
	// or replace the canvas content (false).
	Blend bool
	// Dispose indicates whether the frame area should be cleared to the
	// background color after rendering (true) or left as-is (false).
	Dispose bool
}

var (
	errNotAnimated = errors.New("webp: image is not animated")

	fccANIM = riff.FourCC{'A', 'N', 'I', 'M'}
	fccANMF = riff.FourCC{'A', 'N', 'M', 'F'}
	fccICCP = riff.FourCC{'I', 'C', 'C', 'P'}
	fccXMP  = riff.FourCC{'X', 'M', 'P', ' '}
	fccEXIF = riff.FourCC{'E', 'X', 'I', 'F'}
)

// DecodeAnimated reads an animated WebP image from r and returns its frames
// and metadata. Returns an error if the image is not in the extended file
// format or does not have the animation flag set.
func DecodeAnimated(r io.Reader) (*AnimatedWebP, error) {
	formType, riffReader, err := riff.NewReader(r)
	if err != nil {
		return nil, err
	}
	if formType != fccWEBP {
		return nil, errInvalidFormat
	}

	// VP8X header
	fourCC, chunkLen, chunkData, err := riffReader.Next()
	if err != nil {
		return nil, err
	}
	if fourCC != fccVP8X || chunkLen != 10 {
		return nil, errInvalidFormat
	}
	vp8x := parseVP8X(chunkData)
	if !vp8x.animation {
		return nil, errNotAnimated
	}

	awp := &AnimatedWebP{
		Config: image.Config{
			Width:  int(vp8x.canvasWidth),
			Height: int(vp8x.canvasHeight),
		},
	}

	// Optional ICCP chunk
	if vp8x.iccProfile {
		fourCC, _, chunkData, err = riffReader.Next()
		if err != nil {
			return nil, err
		}
		if fourCC != fccICCP {
			return nil, errInvalidFormat
		}
		awp.ICCP, err = io.ReadAll(chunkData)
		if err != nil {
			return nil, err
		}
	}

	// ANIM header
	fourCC, chunkLen, chunkData, err = riffReader.Next()
	if err != nil {
		return nil, err
	}
	if fourCC != fccANIM || chunkLen != 6 {
		return nil, errInvalidFormat
	}
	bgColor, loopCount, err := parseANIM(chunkData)
	if err != nil {
		return nil, err
	}
	awp.BackgroundColor = bgColor
	awp.LoopCount = loopCount

	// ANMF frames
	for {
		fourCC, chunkLen, chunkData, err = riffReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		switch fourCC {
		case fccANMF:
			frame, err := decodeANMF(chunkData, chunkLen)
			if err != nil {
				return nil, err
			}
			awp.Frames = append(awp.Frames, *frame)
		case fccXMP:
			awp.XMP, err = io.ReadAll(chunkData)
			if err != nil {
				return nil, err
			}
		case fccEXIF:
			awp.EXIF, err = io.ReadAll(chunkData)
			if err != nil {
				return nil, err
			}
		}
	}

	return awp, nil
}

type vp8xInfo struct {
	iccProfile  bool
	alpha       bool
	animation   bool
	canvasWidth uint32
	canvasHeight uint32
}

func parseVP8X(r io.Reader) vp8xInfo {
	var buf [10]byte
	io.ReadFull(r, buf[:])

	const (
		animBit  = 1 << 1
		alphaBit = 1 << 4
		iccBit   = 1 << 5
	)

	w := uint32(buf[4]) | uint32(buf[5])<<8 | uint32(buf[6])<<16
	h := uint32(buf[7]) | uint32(buf[8])<<8 | uint32(buf[9])<<16

	return vp8xInfo{
		iccProfile:   buf[0]&iccBit != 0,
		alpha:        buf[0]&alphaBit != 0,
		animation:    buf[0]&animBit != 0,
		canvasWidth:  w + 1,
		canvasHeight: h + 1,
	}
}

func parseANIM(r io.Reader) (color.Color, uint16, error) {
	var buf [6]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return nil, 0, err
	}
	bg := color.RGBA{
		B: buf[0],
		G: buf[1],
		R: buf[2],
		A: buf[3],
	}
	loopCount := uint16(buf[4]) | uint16(buf[5])<<8
	return bg, loopCount, nil
}

const anmfHeaderSize = 16

func decodeANMF(chunkData io.Reader, chunkLen uint32) (*AnimFrame, error) {
	var buf [anmfHeaderSize]byte
	if _, err := io.ReadFull(chunkData, buf[:]); err != nil {
		return nil, err
	}

	u24 := func(b []byte) uint32 {
		return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
	}

	const (
		disposeBit = 1
		blendBit   = 1 << 1
	)

	frame := &AnimFrame{
		OffsetX:  int(u24(buf[0:3]) * 2),
		OffsetY:  int(u24(buf[3:6]) * 2),
		Width:    int(u24(buf[6:9]) + 1),
		Height:   int(u24(buf[9:12]) + 1),
		Duration: int(u24(buf[12:15])),
		Dispose:  buf[15]&disposeBit != 0,
		Blend:    buf[15]&blendBit == 0,
	}

	// Read sub-chunks (ALPH + VP8/VP8L)
	subData, err := io.ReadAll(chunkData)
	if err != nil {
		return nil, err
	}
	subReader := riff.NewSubChunkReader(bytes.NewReader(subData))

	var (
		alpha       []byte
		alphaStride int
	)

	subFourCC, subChunkData, subChunkLen, err := subReader.Next()
	if err != nil {
		return nil, err
	}

	if subFourCC == fccALPH {
		alpha, alphaStride, err = decodeFrameAlpha(subChunkData, int(subChunkLen), frame)
		if err != nil {
			return nil, err
		}
		// The image sub-chunk follows ALPH, so subChunkLen must be
		// updated to the VP8/VP8L length as well.
		subFourCC, subChunkData, subChunkLen, err = subReader.Next()
		if err != nil {
			return nil, err
		}
	}

	switch subFourCC {
	case fccVP8:
		ycbcr, err := decodeVP8(subChunkData, int(subChunkLen))
		if err != nil {
			return nil, err
		}
		if alpha != nil {
			frame.Image = &image.NYCbCrA{
				YCbCr:   *ycbcr,
				A:       alpha,
				AStride: alphaStride,
			}
		} else {
			frame.Image = ycbcr
		}
	case fccVP8L:
		img, err := vp8l.Decode(subChunkData)
		if err != nil {
			return nil, err
		}
		frame.Image = img
	default:
		return nil, errInvalidFormat
	}

	return frame, nil
}

func decodeVP8(r io.Reader, length int) (*image.YCbCr, error) {
	dec := vp8.NewDecoder()
	dec.Init(r, length)
	if _, err := dec.DecodeFrameHeader(); err != nil {
		return nil, err
	}
	return dec.DecodeFrame()
}

func decodeFrameAlpha(r io.Reader, length int, f *AnimFrame) ([]byte, int, error) {
	var hdr [1]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, 0, err
	}
	buf := make([]byte, length-1)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, 0, err
	}
	compression := hdr[0] & 0x03
	filter := (hdr[0] >> 2) & 0x03
	alpha, stride, err := readAlpha(bytes.NewReader(buf), uint32(f.Width-1), uint32(f.Height-1), compression)
	if err != nil {
		return nil, 0, err
	}
	unfilterAlpha(alpha, stride, filter)
	return alpha, stride, nil
}
