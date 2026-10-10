package receipt

import (
	"bytes"
	"image"
	"image/png"
	"testing"
	"time"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

var msk = time.FixedZone("MSK", 3*3600)

func TestParseQR(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Fiscal
		wantErr bool
		typ     string
	}{
		{
			name: "purchase",
			in:   "t=20261010T0853&s=350.00&fn=9960440300000000&i=12345&fp=1234567890&n=1",
			want: Fiscal{When: time.Date(2026, 10, 10, 8, 53, 0, 0, msk), Cents: 35000, FN: "9960440300000000", I: "12345", FP: "1234567890", N: 1},
			typ:  "withdrawal",
		},
		{
			name: "return",
			in:   "t=20261010T085312&s=1500.50&fn=1&i=2&fp=3&n=2",
			want: Fiscal{When: time.Date(2026, 10, 10, 8, 53, 12, 0, msk), Cents: 150050, FN: "1", I: "2", FP: "3", N: 2},
			typ:  "deposit",
		},
		{
			name: "iso time, comma amount, default n",
			in:   "t=2026-10-10T08:53&s=99,90&fn=1&i=2&fp=3",
			want: Fiscal{When: time.Date(2026, 10, 10, 8, 53, 0, 0, msk), Cents: 9990, FN: "1", I: "2", FP: "3", N: 1},
			typ:  "withdrawal",
		},
		{
			name: "behind url",
			in:   "https://example.com/check?t=20261010T0853&s=10.00&fn=1&i=2&fp=3&n=1",
			want: Fiscal{When: time.Date(2026, 10, 10, 8, 53, 0, 0, msk), Cents: 1000, FN: "1", I: "2", FP: "3", N: 1},
			typ:  "withdrawal",
		},
		{name: "n=4", in: "t=20261010T0853&s=10.00&fn=1&i=2&fp=3&n=4", want: Fiscal{When: time.Date(2026, 10, 10, 8, 53, 0, 0, msk), Cents: 1000, FN: "1", I: "2", FP: "3", N: 4}, typ: "withdrawal"},
		{name: "not fiscal", in: "https://example.com", wantErr: true},
		{name: "bad time", in: "t=garbage&s=10.00&fn=1&i=2&fp=3&n=1", wantErr: true},
		{name: "bad amount", in: "t=20261010T0853&s=abc&fn=1&i=2&fp=3&n=1", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseQR(tt.in, msk)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr {
				return
			}
			if !got.When.Equal(tt.want.When) || got.Cents != tt.want.Cents || got.FN != tt.want.FN ||
				got.I != tt.want.I || got.FP != tt.want.FP || got.N != tt.want.N {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
			if got.Type() != tt.typ {
				t.Errorf("Type = %s, want %s", got.Type(), tt.typ)
			}
		})
	}
	f := Fiscal{FN: "1", I: "2", FP: "3"}
	if f.Ref() != "fn=1 i=2 fp=3" {
		t.Errorf("Ref = %q", f.Ref())
	}
}

func qrPNG(t *testing.T, text string, size int) []byte {
	t.Helper()
	m, err := qrcode.NewQRCodeWriter().EncodeWithoutHint(text, gozxing.BarcodeFormat_QR_CODE, size, size)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewGray(image.Rect(0, 0, m.GetWidth(), m.GetHeight()))
	for y := 0; y < m.GetHeight(); y++ {
		for x := 0; x < m.GetWidth(); x++ {
			if m.Get(x, y) {
				img.Pix[y*img.Stride+x] = 0
			} else {
				img.Pix[y*img.Stride+x] = 255
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecodeQR(t *testing.T) {
	payload := "t=20261010T0853&s=350.00&fn=9960440300000000&i=12345&fp=1234567890&n=1"
	for _, size := range []int{300, 2200} { // 2200 exercises downscaling
		data := qrPNG(t, payload, size)
		got, err := DecodeQR(data)
		if err != nil || got != payload {
			t.Fatalf("size %d: got %q, %v", size, got, err)
		}
	}
	if _, err := DecodeQR([]byte("not an image")); err != ErrNoQR {
		t.Errorf("garbage: %v", err)
	}
	blank := image.NewGray(image.Rect(0, 0, 200, 200))
	var buf bytes.Buffer
	_ = png.Encode(&buf, blank)
	if _, err := DecodeQR(buf.Bytes()); err != ErrNoQR {
		t.Errorf("blank: %v", err)
	}
}
