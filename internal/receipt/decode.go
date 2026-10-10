package receipt

import (
	"bytes"
	"errors"
	"image"
	"image/draw"
	"strings"

	// Image formats accepted from Telegram.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

// ErrNoQR is returned when no QR code could be found.
var ErrNoQR = errors.New("no QR code found")

// maxScan limits work spent per file.
const maxScan = 25 << 20

// DecodeQR finds a QR code in an image (or in images embedded in a PDF)
// and returns its text. data is the raw file content.
func DecodeQR(data []byte) (string, error) {
	if len(data) > maxScan {
		return "", ErrNoQR
	}
	if bytes.HasPrefix(data, []byte("%PDF")) {
		for _, img := range pdfJPEGs(data) {
			if s, err := decodeImage(img); err == nil {
				return s, nil
			}
		}
		return "", ErrNoQR
	}
	return decodeImage(data)
}

func decodeImage(data []byte) (string, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", ErrNoQR
	}
	for _, size := range []int{0, 1600, 1000, 600} {
		candidate := img
		if size > 0 {
			b := img.Bounds()
			if b.Dx() <= size && b.Dy() <= size {
				continue
			}
			candidate = downscale(img, size)
		}
		if s, ok := tryDecode(candidate); ok {
			return s, nil
		}
	}
	return "", ErrNoQR
}

func tryDecode(img image.Image) (string, bool) {
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return "", false
	}
	hints := map[gozxing.DecodeHintType]interface{}{
		gozxing.DecodeHintType_TRY_HARDER:       true,
		gozxing.DecodeHintType_CHARACTER_SET:    "UTF-8",
		gozxing.DecodeHintType_POSSIBLE_FORMATS: []gozxing.BarcodeFormat{gozxing.BarcodeFormat_QR_CODE},
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, hints)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(res.GetText()), true
}

// downscale shrinks img so that its longest side is at most size, using box sampling.
func downscale(src image.Image, size int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := float64(size) / float64(max(w, h))
	nw, nh := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
	dst := image.NewGray(image.Rect(0, 0, nw, nh))
	gray := image.NewGray(b)
	draw.Draw(gray, b, src, b.Min, draw.Src)
	for y := 0; y < nh; y++ {
		sy0, sy1 := y*h/nh, max(y*h/nh+1, (y+1)*h/nh)
		for x := 0; x < nw; x++ {
			sx0, sx1 := x*w/nw, max(x*w/nw+1, (x+1)*w/nw)
			var sum, n int
			for yy := sy0; yy < sy1; yy++ {
				for xx := sx0; xx < sx1; xx++ {
					sum += int(gray.GrayAt(b.Min.X+xx, b.Min.Y+yy).Y)
					n++
				}
			}
			dst.SetGray(x, y, grayColor(uint8(sum/n)))
		}
	}
	return dst
}

// pdfJPEGs extracts DCT (JPEG) streams from a PDF. It is a best-effort
// shortcut for scanned or exported receipts, not a PDF parser.
func pdfJPEGs(pdf []byte) [][]byte {
	var out [][]byte
	soi := []byte{0xFF, 0xD8, 0xFF}
	eoi := []byte{0xFF, 0xD9}
	rest := pdf
	for len(out) < 8 {
		i := bytes.Index(rest, soi)
		if i < 0 {
			break
		}
		j := bytes.Index(rest[i:], []byte("endstream"))
		if j < 0 {
			break
		}
		chunk := rest[i : i+j]
		if k := bytes.LastIndex(chunk, eoi); k > 0 {
			out = append(out, chunk[:k+2])
		}
		rest = rest[i+j:]
	}
	return out
}
