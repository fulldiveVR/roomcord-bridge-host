// roomcord-bridge-host - A host process for the mautrix bridgev2 network connectors.
// Copyright (C) 2026 The roomcord-bridge-host authors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, version 3 of the License.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package media makes the FakeChat media files. Every file is deterministic: the
// same name gives the same bytes on every run and every machine. Small video,
// voice and WebP files are embedded (assets.go); the other files are generated
// on first use, also the large ones, so no big binary is in the repository.
package media

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/proto"
)

// RelayLimit is the attachment limit of the desktop relay (8 MiB, SPEC 5.7).
const RelayLimit = 8 << 20

// Entry is one file of the catalog.
type Entry struct {
	Name       string
	Kind       string
	Mime       string
	Width      int
	Height     int
	DurationMs int
	gen        func() []byte

	once sync.Once
	data []byte
}

// Data returns the bytes, generated on the first call.
func (e *Entry) Data() []byte {
	e.once.Do(func() { e.data = e.gen() })
	return e.data
}

// Meta is the API shape of the entry, with the given media id.
func (e *Entry) Meta(id string) proto.Media {
	return proto.Media{ID: id, Kind: e.Kind, Name: e.Name, Mime: e.Mime, Size: int64(len(e.Data())),
		Width: e.Width, Height: e.Height, DurationMs: e.DurationMs}
}

// Catalog names. Scenarios and the control API use them.
const (
	PhotoJPG      = "photo.jpg"
	PhotoPNG      = "photo.png"
	AnimGIF       = "anim.gif"
	PhotoWebP     = "photo.webp"
	ClipMP4       = "clip.mp4"
	VoiceOGG      = "voice.ogg"
	ToneWAV       = "tone.wav"
	DocPDF        = "doc.pdf"
	ArchiveZIP    = "archive.zip"
	StickerWebP   = "sticker.webp"
	LimitBin      = "limit-8mb.bin"
	OversizeBin   = "oversize-9mb.bin"
	OversizePhoto = "oversize-photo.bmp"
)

var catalog = map[string]*Entry{}

func add(e *Entry) { catalog[e.Name] = e }

func init() {
	add(&Entry{Name: PhotoJPG, Kind: proto.KindImage, Mime: "image/jpeg", Width: 320, Height: 240, gen: func() []byte { return encodeJPEG(gradient(320, 240)) }})
	add(&Entry{Name: PhotoPNG, Kind: proto.KindImage, Mime: "image/png", Width: 256, Height: 256, gen: func() []byte { return encodePNG(checker(256, 256, 32)) }})
	add(&Entry{Name: AnimGIF, Kind: proto.KindImage, Mime: "image/gif", Width: 64, Height: 64, DurationMs: 900, gen: animGIF})
	add(&Entry{Name: PhotoWebP, Kind: proto.KindImage, Mime: "image/webp", Width: 64, Height: 48, gen: embedded(assetphotoWebP)})
	add(&Entry{Name: ClipMP4, Kind: proto.KindVideo, Mime: "video/mp4", Width: 96, Height: 64, DurationMs: 1000, gen: embedded(assetclipMP4)})
	add(&Entry{Name: VoiceOGG, Kind: proto.KindVoice, Mime: "audio/ogg", DurationMs: 1506, gen: embedded(assetvoiceOGG)})
	add(&Entry{Name: ToneWAV, Kind: proto.KindAudio, Mime: "audio/wav", DurationMs: 2000, gen: func() []byte { return wav(2000, 8000, 523.25) }})
	add(&Entry{Name: DocPDF, Kind: proto.KindFile, Mime: "application/pdf", gen: pdf})
	add(&Entry{Name: ArchiveZIP, Kind: proto.KindFile, Mime: "application/zip", gen: zipFile})
	add(&Entry{Name: StickerWebP, Kind: proto.KindSticker, Mime: "image/webp", Width: 128, Height: 128, gen: embedded(assetstickerWebP)})
	add(&Entry{Name: LimitBin, Kind: proto.KindFile, Mime: "application/octet-stream", gen: func() []byte { return noise(RelayLimit, 8) }})
	add(&Entry{Name: OversizeBin, Kind: proto.KindFile, Mime: "application/octet-stream", gen: func() []byte { return noise(9<<20, 9) }})
	add(&Entry{Name: OversizePhoto, Kind: proto.KindImage, Mime: "image/bmp", Width: 1800, Height: 1600, gen: func() []byte { return bmp(1800, 1600) }})
}

// Get returns a catalog entry by name.
func Get(name string) (*Entry, bool) {
	e, ok := catalog[name]
	return e, ok
}

// Names lists the catalog, sorted.
func Names() []string {
	out := make([]string, 0, len(catalog))
	for n := range catalog {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Avatar is a 96x96 PNG in the given color with a lighter inner square.
func Avatar(c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 96, 96))
	light := color.RGBA{R: c.R/2 + 127, G: c.G/2 + 127, B: c.B/2 + 127, A: 255}
	for y := range 96 {
		for x := range 96 {
			if x >= 28 && x < 68 && y >= 28 && y < 68 {
				img.Set(x, y, light)
			} else {
				img.Set(x, y, c)
			}
		}
	}
	return encodePNG(img)
}

func embedded(b64 string) func() []byte {
	return func() []byte {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			panic(err)
		}
		return data
	}
}

func gradient(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 160, A: 255})
		}
	}
	return img
}

func checker(w, h, cell int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if (x/cell+y/cell)%2 == 0 {
				img.Set(x, y, color.RGBA{R: 30, G: 110, B: 200, A: 255})
			} else {
				img.Set(x, y, color.RGBA{R: 250, G: 250, B: 250, A: 255})
			}
		}
	}
	return img
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func encodeJPEG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func animGIF() []byte {
	anim := &gif.GIF{}
	for i := range 3 {
		img := image.NewPaletted(image.Rect(0, 0, 64, 64), palette.Plan9)
		for y := range 64 {
			for x := range 64 {
				if (x+y+i*16)%48 < 24 {
					img.Set(x, y, color.RGBA{R: 220, G: 60, B: 60, A: 255})
				} else {
					img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
				}
			}
		}
		anim.Image = append(anim.Image, img)
		anim.Delay = append(anim.Delay, 30)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// wav is a mono 16-bit PCM sine tone.
func wav(durationMs, rate int, freq float64) []byte {
	samples := rate * durationMs / 1000
	var buf bytes.Buffer
	le := binary.LittleEndian
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, le, uint32(36+samples*2))
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(&buf, le, uint32(16))
	_ = binary.Write(&buf, le, uint16(1))
	_ = binary.Write(&buf, le, uint16(1))
	_ = binary.Write(&buf, le, uint32(rate))
	_ = binary.Write(&buf, le, uint32(rate*2))
	_ = binary.Write(&buf, le, uint16(2))
	_ = binary.Write(&buf, le, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(&buf, le, uint32(samples*2))
	for i := range samples {
		v := int16(math.Round(8000 * math.Sin(2*math.Pi*freq*float64(i)/float64(rate))))
		_ = binary.Write(&buf, le, v)
	}
	return buf.Bytes()
}

// pdf is a one-page PDF with one line of text and a correct xref table.
func pdf() []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		"",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	stream := "BT /F1 18 Tf 24 70 Td (FakeChat test document) Tj ET"
	objs[3] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream)
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return buf.Bytes()
}

func zipFile() []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	modified := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, f := range []struct{ name, body string }{
		{"readme.txt", "FakeChat test archive.\n"},
		{"data/numbers.csv", "n,square\n1,1\n2,4\n3,9\n"},
	} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: modified})
		if err != nil {
			panic(err)
		}
		_, _ = w.Write([]byte(f.body))
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// noise is size bytes from a fixed xorshift sequence (not compressible, so the
// size stays the same after any transport compression).
func noise(size int, seed uint64) []byte {
	out := make([]byte, size)
	x := seed*0x9E3779B97F4A7C15 + 1
	for i := 0; i < size; i += 8 {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], x)
		copy(out[i:], b[:])
	}
	return out
}

// bmp is an uncompressed 24-bit BMP. Width*3 must be a multiple of 4.
func bmp(w, h int) []byte {
	row := w * 3
	size := 54 + row*h
	out := make([]byte, size)
	le := binary.LittleEndian
	copy(out, "BM")
	le.PutUint32(out[2:], uint32(size))
	le.PutUint32(out[10:], 54)
	le.PutUint32(out[14:], 40)
	le.PutUint32(out[18:], uint32(w))
	le.PutUint32(out[22:], uint32(h))
	le.PutUint16(out[26:], 1)
	le.PutUint16(out[28:], 24)
	le.PutUint32(out[34:], uint32(row*h))
	p := 54
	for y := range h {
		for x := range w {
			out[p] = uint8(y * 255 / h)
			out[p+1] = uint8((x ^ y) & 0xff)
			out[p+2] = uint8(x * 255 / w)
			p += 3
		}
	}
	return out
}
