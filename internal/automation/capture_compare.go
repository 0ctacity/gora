package automation

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

type CompareOptions struct {
	ChannelTolerance int
	MaxChangedPixels int
	Masks            []image.Rectangle
}

type CompareResult struct {
	Passed            bool
	Width             int
	Height            int
	ReferenceWidth    int
	ReferenceHeight   int
	DimensionMismatch bool
	ChangedPixels     int
	ChangedRatio      float64
	MaximumDelta      int
	ChangedBounds     image.Rectangle
	CurrentPNG        []byte
	DiffPNG           []byte
}

// ComparePNG decodes both PNGs into non-premultiplied NRGBA pixels and compares
// the same logical image coordinates. Masks are applied after exact scale
// normalization by the caller.
func ComparePNG(reference, current []byte, options CompareOptions) (CompareResult, error) {
	if options.ChannelTolerance < 0 || options.ChannelTolerance > 255 {
		return CompareResult{}, fmt.Errorf("channel tolerance must be between 0 and 255")
	}
	if options.MaxChangedPixels < 0 {
		return CompareResult{}, fmt.Errorf("max changed pixels must be non-negative")
	}
	referenceImage, err := png.Decode(bytes.NewReader(reference))
	if err != nil {
		return CompareResult{}, fmt.Errorf("decode reference PNG: %w", err)
	}
	currentImage, err := png.Decode(bytes.NewReader(current))
	if err != nil {
		return CompareResult{}, fmt.Errorf("decode current PNG: %w", err)
	}
	referenceBounds, currentBounds := referenceImage.Bounds(), currentImage.Bounds()
	if referenceBounds.Dx() != currentBounds.Dx() || referenceBounds.Dy() != currentBounds.Dy() {
		width, height := currentBounds.Dx(), currentBounds.Dy()
		diff := image.NewNRGBA(image.Rect(0, 0, width, height))
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, diff); err != nil {
			return CompareResult{}, err
		}
		return CompareResult{Passed: false, Width: width, Height: height, ReferenceWidth: referenceBounds.Dx(), ReferenceHeight: referenceBounds.Dy(), DimensionMismatch: true, CurrentPNG: append([]byte(nil), current...), DiffPNG: buffer.Bytes()}, nil
	}
	width, height := currentBounds.Dx(), currentBounds.Dy()
	diff := image.NewNRGBA(image.Rect(0, 0, width, height))
	changed := 0
	maximum := 0
	var bounds image.Rectangle
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if masked(options.Masks, x, y) {
				continue
			}
			referenceColor := color.NRGBAModel.Convert(referenceImage.At(referenceBounds.Min.X+x, referenceBounds.Min.Y+y)).(color.NRGBA)
			currentColor := color.NRGBAModel.Convert(currentImage.At(currentBounds.Min.X+x, currentBounds.Min.Y+y)).(color.NRGBA)
			delta := maxChannelDelta(referenceColor, currentColor)
			if delta <= options.ChannelTolerance {
				continue
			}
			changed++
			if delta > maximum {
				maximum = delta
			}
			point := image.Pt(x, y)
			if bounds.Empty() {
				bounds = image.Rectangle{Min: point, Max: point.Add(image.Pt(1, 1))}
			} else {
				bounds = bounds.Union(image.Rectangle{Min: point, Max: point.Add(image.Pt(1, 1))})
			}
			// A stable red marker makes failures actionable while keeping the
			// unchanged pixels transparent and the output bounded to the image.
			diff.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, diff); err != nil {
		return CompareResult{}, err
	}
	result := CompareResult{Passed: changed <= options.MaxChangedPixels, Width: width, Height: height, ReferenceWidth: width, ReferenceHeight: height, ChangedPixels: changed, MaximumDelta: maximum, ChangedBounds: bounds, CurrentPNG: append([]byte(nil), current...), DiffPNG: buffer.Bytes()}
	if width > 0 && height > 0 {
		result.ChangedRatio = float64(changed) / float64(width*height)
	}
	return result, nil
}

func masked(masks []image.Rectangle, x, y int) bool {
	point := image.Pt(x, y)
	for _, mask := range masks {
		if point.In(mask) {
			return true
		}
	}
	return false
}

func maxChannelDelta(a, b color.NRGBA) int {
	maximum := absInt(int(a.R) - int(b.R))
	if value := absInt(int(a.G) - int(b.G)); value > maximum {
		maximum = value
	}
	if value := absInt(int(a.B) - int(b.B)); value > maximum {
		maximum = value
	}
	if value := absInt(int(a.A) - int(b.A)); value > maximum {
		maximum = value
	}
	return maximum
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
