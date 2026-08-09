package automation

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestComparePNGExactToleranceMasksAndBounds(t *testing.T) {
	var reference []byte
	referenceImage := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			referenceImage.SetNRGBA(x, y, color.NRGBA{A: 255})
		}
	}
	reference = testPNG(t, referenceImage)
	currentImage := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			currentImage.SetNRGBA(x, y, color.NRGBA{A: 255})
		}
	}
	currentImage.SetNRGBA(1, 0, color.NRGBA{R: 10, A: 255})
	current := testPNG(t, currentImage)
	result, err := ComparePNG(reference, current, CompareOptions{ChannelTolerance: 0})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || result.ChangedPixels != 1 || result.MaximumDelta != 10 || result.ChangedBounds != (image.Rectangle{Min: image.Pt(1, 0), Max: image.Pt(2, 1)}) {
		t.Fatalf("comparison = %+v", result)
	}
	if result.DiffPNG == nil || !bytes.HasPrefix(result.DiffPNG, []byte("\x89PNG")) {
		t.Fatalf("missing deterministic diff PNG")
	}
	result, err = ComparePNG(reference, current, CompareOptions{ChannelTolerance: 10})
	if err != nil || !result.Passed || result.ChangedPixels != 0 {
		t.Fatalf("tolerance comparison = %+v err=%v", result, err)
	}
	result, err = ComparePNG(reference, current, CompareOptions{Masks: []image.Rectangle{{Min: image.Pt(1, 0), Max: image.Pt(2, 1)}}})
	if err != nil || !result.Passed {
		t.Fatalf("mask comparison = %+v err=%v", result, err)
	}
	result, err = ComparePNG(reference, current, CompareOptions{MaxChangedPixels: 1})
	if err != nil || !result.Passed {
		t.Fatalf("changed-pixel threshold = %+v err=%v", result, err)
	}
}

func TestComparePNGRejectsDimensionMismatch(t *testing.T) {
	reference := testPNG(t, image.NewNRGBA(image.Rect(0, 0, 2, 2)))
	current := testPNG(t, image.NewNRGBA(image.Rect(0, 0, 3, 2)))
	result, err := ComparePNG(reference, current, CompareOptions{})
	if err != nil || result.Passed || !result.DimensionMismatch || result.ReferenceWidth != 2 || result.Width != 3 {
		t.Fatalf("dimension mismatch result = %+v err=%v", result, err)
	}
}

func TestComparePNGIsDeterministicAcrossRuns(t *testing.T) {
	reference := testPNG(t, image.NewNRGBA(image.Rect(0, 0, 2, 2)))
	currentImage := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	currentImage.SetNRGBA(1, 1, color.NRGBA{B: 255, A: 255})
	current := testPNG(t, currentImage)
	one, err := ComparePNG(reference, current, CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	two, err := ComparePNG(reference, current, CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one.DiffPNG, two.DiffPNG) || one.ChangedPixels != two.ChangedPixels || one.ChangedBounds != two.ChangedBounds || one.MaximumDelta != two.MaximumDelta {
		t.Fatalf("nondeterministic comparison: one=%+v two=%+v", one, two)
	}
}

func TestComparePNGChangedMetricsMasksAndAlphaMatrix(t *testing.T) {
	referenceImage := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			referenceImage.SetNRGBA(x, y, color.NRGBA{A: 255})
		}
	}
	currentImage := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			currentImage.SetNRGBA(x, y, color.NRGBA{A: 255})
		}
	}
	currentImage.SetNRGBA(1, 0, color.NRGBA{R: 10, A: 255})
	currentImage.SetNRGBA(3, 1, color.NRGBA{G: 240, A: 255})
	one, err := ComparePNG(testPNG(t, referenceImage), testPNG(t, currentImage), CompareOptions{MaxChangedPixels: 1})
	if err != nil || one.Passed || one.ChangedPixels != 2 || one.MaximumDelta != 240 || one.ChangedRatio != 0.25 || one.ChangedBounds != image.Rect(1, 0, 4, 2) {
		t.Fatalf("changed metrics = %+v err=%v", one, err)
	}
	two, err := ComparePNG(testPNG(t, referenceImage), testPNG(t, currentImage), CompareOptions{MaxChangedPixels: 2})
	if err != nil || !two.Passed {
		t.Fatalf("max changed boundary = %+v err=%v", two, err)
	}
	masked, err := ComparePNG(testPNG(t, referenceImage), testPNG(t, currentImage), CompareOptions{Masks: []image.Rectangle{image.Rect(1, 0, 2, 1), image.Rect(3, 1, 4, 2), image.Rect(99, 99, 100, 100), image.Rectangle{}}})
	if err != nil || !masked.Passed || masked.ChangedPixels != 0 {
		t.Fatalf("mask edge behavior = %+v err=%v", masked, err)
	}
	// NRGBA conversion compares unpremultiplied channels rather than the
	// implementation-dependent premultiplied representation returned by At.
	nrgba := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	nrgba.SetNRGBA(0, 0, color.NRGBA{R: 128, G: 64, B: 32, A: 128})
	rgba := image.NewRGBA(image.Rect(0, 0, 1, 1))
	rgba.Set(0, 0, color.NRGBA{R: 128, G: 64, B: 32, A: 128})
	equivalent, err := ComparePNG(testPNG(t, nrgba), testPNG(t, rgba), CompareOptions{ChannelTolerance: 1})
	if err != nil || !equivalent.Passed || equivalent.ChangedPixels != 0 {
		t.Fatalf("equivalent alpha representations differ: %+v err=%v", equivalent, err)
	}
	altered := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	altered.SetNRGBA(0, 0, color.NRGBA{R: 64, G: 32, B: 16, A: 128})
	alphaResult, err := ComparePNG(testPNG(t, nrgba), testPNG(t, altered), CompareOptions{})
	if err != nil || alphaResult.Passed || alphaResult.ChangedPixels != 1 || alphaResult.MaximumDelta != 64 {
		t.Fatalf("alpha channel delta was not measured in NRGBA space: %+v err=%v", alphaResult, err)
	}
}

func testPNG(t *testing.T, imageValue image.Image) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, imageValue); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
