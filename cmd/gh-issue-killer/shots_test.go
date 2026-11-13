package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func writeSolid(t *testing.T, file string, width, height int, c color.Color) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, c)
		}
	}
	handle, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if err := png.Encode(handle, img); err != nil {
		t.Fatal(err)
	}
}

func TestComposeBeforeAfter(t *testing.T) {
	dir := t.TempDir()
	before := filepath.Join(dir, "before.png")
	after := filepath.Join(dir, "after.png")
	out := filepath.Join(dir, "combined.png")
	writeSolid(t, before, 100, 50, color.RGBA{R: 255, A: 255})
	writeSolid(t, after, 80, 70, color.RGBA{B: 255, A: 255})

	if err := composeBeforeAfter(before, after, out); err != nil {
		t.Fatal(err)
	}
	handle, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	img, _, err := image.Decode(handle)
	if err != nil {
		t.Fatal(err)
	}
	// 100 + 12px gap + 80, padded to the taller 70px height.
	if img.Bounds().Dx() != 192 || img.Bounds().Dy() != 70 {
		t.Fatalf("combined size = %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestScreenshotsMarkdown(t *testing.T) {
	md := screenshotsMarkdown([]Shot{{Caption: "顶部工具栏", URL: "https://example.com/a.png"}})
	if md == "" {
		t.Fatal("expected markdown")
	}
	for _, want := range []string{"顶部工具栏", "左：改动前", "https://example.com/a.png"} {
		if !contains(md, want) {
			t.Fatalf("markdown missing %q: %s", want, md)
		}
	}
	if screenshotsMarkdown(nil) != "" {
		t.Fatal("empty shots should produce empty markdown")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
