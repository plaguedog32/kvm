// Package videoimage encodes the capture device's packed YUYV frames as JPEG.
package videoimage

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
)

// JPEG converts limited-range YUYV (BT.709 for HD, BT.601 for SD) to the
// full-range BT.601 YCbCr expected by JPEG. No DMA memory is retained.
func JPEG(ctx context.Context, raw []byte, width, height int) ([]byte, error) {
	if width <= 0 || width > 4096 || height <= 0 || height > 2160 || width%2 != 0 || len(raw) != width*height*2 {
		return nil, fmt.Errorf("invalid YUYV frame dimensions or length")
	}
	img := image.NewYCbCr(image.Rect(0, 0, width, height), image.YCbCrSubsampleRatio422)
	for y := 0; y < height; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := 0; x < width; x += 2 {
			i := (y*width + x) * 2
			r0, g0, b0 := rgb(raw[i], raw[i+1], raw[i+3], height > 576)
			r1, g1, b1 := rgb(raw[i+2], raw[i+1], raw[i+3], height > 576)
			y0, cb0, cr0 := color.RGBToYCbCr(r0, g0, b0)
			y1, cb1, cr1 := color.RGBToYCbCr(r1, g1, b1)
			img.Y[y*img.YStride+x], img.Y[y*img.YStride+x+1] = y0, y1
			ci := y*img.CStride + x/2
			img.Cb[ci] = byte((int(cb0) + int(cb1) + 1) / 2)
			img.Cr[ci] = byte((int(cr0) + int(cr1) + 1) / 2)
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func rgb(y, u, v byte, hd bool) (byte, byte, byte) {
	c, d, e := int(y)-16, int(u)-128, int(v)-128
	if hd {
		return clamp((298*c + 459*e + 128) >> 8), clamp((298*c - 55*d - 136*e + 128) >> 8), clamp((298*c + 541*d + 128) >> 8)
	}
	return clamp((298*c + 409*e + 128) >> 8), clamp((298*c - 100*d - 208*e + 128) >> 8), clamp((298*c + 516*d + 128) >> 8)
}

func clamp(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}
