package videoimage

import (
	"bytes"
	"context"
	"image/color"
	"image/jpeg"
	"testing"
)

func TestJPEGLevelsAndDimensions(t *testing.T) {
	for _, tc := range []struct {
		name string
		y    byte
		want int
	}{{"black", 16, 0}, {"white", 235, 255}} {
		t.Run(tc.name, func(t *testing.T) {
			raw := bytes.Repeat([]byte{tc.y, 128, tc.y, 128}, 8*8/2)
			data, err := JPEG(context.Background(), raw, 8, 8)
			if err != nil {
				t.Fatal(err)
			}
			img, err := jpeg.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if img.Bounds().Dx() != 8 || img.Bounds().Dy() != 8 {
				t.Fatal(img.Bounds())
			}
			rgb := color.RGBAModel.Convert(img.At(4, 4)).(color.RGBA)
			for _, v := range []byte{rgb.R, rgb.G, rgb.B} {
				if delta := int(v) - tc.want; delta < -2 || delta > 2 {
					t.Fatalf("pixel=%v want %d", rgb, tc.want)
				}
			}
		})
	}
}

func TestColorimetry(t *testing.T) {
	// Limited-range nominal red for BT.709 and BT.601 respectively.
	for _, tc := range []struct {
		y, u, v byte
		hd      bool
	}{{63, 102, 240, true}, {81, 90, 240, false}} {
		r, g, b := rgb(tc.y, tc.u, tc.v, tc.hd)
		if r < 250 || g > 3 || b > 3 {
			t.Fatalf("red converted to %d,%d,%d", r, g, b)
		}
	}
}

func TestRejectInvalidAndCanceledFrames(t *testing.T) {
	for _, size := range [][2]int{{0, 2}, {3, 2}, {4098, 2}, {2, 2161}, {2, 2}} {
		if _, err := JPEG(context.Background(), nil, size[0], size[1]); err == nil {
			t.Fatal("accepted invalid frame", size)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := JPEG(ctx, make([]byte, 8), 2, 2); err != context.Canceled {
		t.Fatal(err)
	}
}
