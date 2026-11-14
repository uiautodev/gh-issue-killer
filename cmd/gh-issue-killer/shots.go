package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

type ResultManifest struct {
	UIChanged   *bool             `json:"uiChanged"`
	Summary     string            `json:"summary"`
	Changes     []string          `json:"changes"`
	Tests       []string          `json:"tests"`
	Screenshots []json.RawMessage `json:"screenshots"`
	PRTitle     string            `json:"prTitle"`
	PRBody      string            `json:"prBody"`
	// Abort tells the harness the issue could not be reproduced; no code was
	// changed and no PR should be opened.
	Abort bool `json:"abort,omitempty"`
}

type ShotEntry struct {
	Caption  string `json:"caption"`
	Before   string `json:"before"`
	After    string `json:"after"`
	Combined string `json:"combined"`
}

func loadManifest(runDir string) ResultManifest {
	var manifest ResultManifest
	_ = readJSONIfExists(filepath.Join(runDir, "result.json"), &manifest)
	return manifest
}

func resolveRunFile(runDir, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	base, err := filepath.Abs(runDir)
	if err != nil {
		return ""
	}
	file := value
	if !filepath.IsAbs(file) {
		file = filepath.Join(base, value)
	}
	file, err = filepath.Abs(file)
	if err != nil {
		return ""
	}
	if file != base && !strings.HasPrefix(file, base+string(os.PathSeparator)) {
		return ""
	}
	if !fileExists(file) {
		return ""
	}
	return file
}

// composeBeforeAfter stacks before above after on a white canvas, separated
// by a light gutter with a dark divider so it is obvious which half is the old
// version. Stacking keeps each screenshot at full width, which matters on a
// phone where a side-by-side pair is unreadably small. Pure stdlib image code.
func composeBeforeAfter(beforeFile, afterFile, outFile string) error {
	before, err := decodeImage(beforeFile)
	if err != nil {
		return fmt.Errorf("decode before: %w", err)
	}
	after, err := decodeImage(afterFile)
	if err != nil {
		return fmt.Errorf("decode after: %w", err)
	}

	beforeBounds := before.Bounds()
	afterBounds := after.Bounds()
	beforeWidth, beforeHeight := beforeBounds.Dx(), beforeBounds.Dy()
	afterWidth, afterHeight := afterBounds.Dx(), afterBounds.Dy()
	width := beforeWidth
	if afterWidth > width {
		width = afterWidth
	}
	const (
		gap     = 20
		divider = 2
	)
	height := beforeHeight + gap + afterHeight

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)

	gutter := image.Rect(0, beforeHeight, width, beforeHeight+gap)
	draw.Draw(canvas, gutter, &image.Uniform{C: color.RGBA{R: 0xe2, G: 0xe8, B: 0xf0, A: 0xff}}, image.Point{}, draw.Src)
	lineY := beforeHeight + (gap-divider)/2
	draw.Draw(canvas, image.Rect(0, lineY, width, lineY+divider), &image.Uniform{C: color.RGBA{R: 0x47, G: 0x55, B: 0x69, A: 0xff}}, image.Point{}, draw.Src)

	beforeLeft := (width - beforeWidth) / 2
	afterLeft := (width - afterWidth) / 2
	afterTop := beforeHeight + gap
	draw.Draw(canvas, image.Rect(beforeLeft, 0, beforeLeft+beforeWidth, beforeHeight), before, beforeBounds.Min, draw.Over)
	draw.Draw(canvas, image.Rect(afterLeft, afterTop, afterLeft+afterWidth, afterTop+afterHeight), after, afterBounds.Min, draw.Over)

	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return err
	}
	handle, err := os.Create(outFile)
	if err != nil {
		return err
	}
	defer handle.Close()
	return png.Encode(handle, canvas)
}

func decodeImage(file string) (image.Image, error) {
	handle, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	img, _, err := image.Decode(handle)
	return img, err
}

func parseShotEntry(raw json.RawMessage) (ShotEntry, error) {
	var path string
	if err := json.Unmarshal(raw, &path); err == nil {
		return ShotEntry{After: path}, nil
	}
	var entry ShotEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return ShotEntry{}, err
	}
	return entry, nil
}

func collectScreenshots(ctx context.Context, gh *GH, runDir string, manifest ResultManifest, repoID int64, log *Logger) []Shot {
	if len(manifest.Screenshots) == 0 {
		return nil
	}
	shots := make([]Shot, 0, len(manifest.Screenshots))
	for index, raw := range manifest.Screenshots {
		entry, err := parseShotEntry(raw)
		if err != nil {
			log.Warn("invalid screenshot entry: %v", err)
			continue
		}
		caption := strings.TrimSpace(entry.Caption)
		if caption == "" {
			caption = fmt.Sprintf("UI change %d", index+1)
		}

		file := ""
		switch {
		case entry.Combined != "":
			file = resolveRunFile(runDir, entry.Combined)
		case entry.Before != "" && entry.After != "":
			before := resolveRunFile(runDir, entry.Before)
			after := resolveRunFile(runDir, entry.After)
			if before == "" || after == "" {
				log.Warn("screenshot pair %q missing before/after file", caption)
				continue
			}
			file = filepath.Join(runDir, "screenshots", fmt.Sprintf("combined-%02d.png", index+1))
			if err := composeBeforeAfter(before, after, file); err != nil {
				log.Warn("compose %q: %v", caption, err)
				continue
			}
			log.Info("composed before/after: %s", file)
		case entry.After != "":
			file = resolveRunFile(runDir, entry.After)
			log.Warn("screenshot %q has no before image, uploading after only", caption)
		}

		if file == "" {
			log.Warn("screenshot %q not found", caption)
			continue
		}
		info, err := os.Stat(file)
		if err != nil || info.Size() == 0 {
			continue
		}
		url, err := gh.UploadAttachment(ctx, repoID, file)
		if err != nil {
			log.Warn("upload screenshot %s: %v", filepath.Base(file), err)
			continue
		}
		shots = append(shots, Shot{Name: filepath.Base(file), Caption: caption, URL: url, Size: info.Size()})
		log.Info("screenshot uploaded: %s -> %s", filepath.Base(file), url)
	}
	return shots
}

func screenshotsMarkdown(shots []Shot) string {
	if len(shots) == 0 {
		return ""
	}
	parts := make([]string, 0, len(shots))
	for _, shot := range shots {
		parts = append(parts, fmt.Sprintf("**%s**（上：改动前 / 下：改动后）\n\n![%s](%s)", shot.Caption, shot.Caption, shot.URL))
	}
	return strings.Join(parts, "\n\n")
}
