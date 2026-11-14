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
	// Full width of the wider image, stacked: 50 + 20px gap + 70.
	if img.Bounds().Dx() != 100 || img.Bounds().Dy() != 140 {
		t.Fatalf("combined size = %dx%d, want 100x140", img.Bounds().Dx(), img.Bounds().Dy())
	}
	// Before on top, after below the gutter.
	if r, _, _, _ := img.At(10, 10).RGBA(); r>>8 < 0xf0 {
		t.Fatalf("before image should be at the top, got %#x", r>>8)
	}
	if _, _, b, _ := img.At(10, 80).RGBA(); b>>8 < 0xf0 {
		t.Fatalf("after image should be below the gutter, got %#x", b>>8)
	}
	// The gutter is light grey and carries a dark divider across it.
	if r, _, _, _ := img.At(50, 52).RGBA(); r>>8 < 0xd0 {
		t.Fatalf("gutter should be light grey, got %#x", r>>8)
	}
	if r, g, b, _ := img.At(50, 59).RGBA(); r>>8 > 0x80 || g>>8 > 0x80 || b>>8 > 0x80 {
		t.Fatalf("divider should be dark, got %#x %#x %#x", r>>8, g>>8, b>>8)
	}
}

func TestScreenshotsMarkdown(t *testing.T) {
	md := screenshotsMarkdown([]Shot{{Caption: "顶部工具栏", URL: "https://example.com/a.png"}})
	if md == "" {
		t.Fatal("expected markdown")
	}
	for _, want := range []string{"顶部工具栏", "上：改动前", "https://example.com/a.png"} {
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
