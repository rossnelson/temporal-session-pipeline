package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.temporal.io/sdk/activity"
)

// ExtractClipsActivity uses FFmpeg to extract clips at highlight timestamps.
// Detects whether the source is audio or video and extracts accordingly.
func ExtractClipsActivity(ctx context.Context, input ExtractClipsInput) (*ExtractClipsResult, error) {
	logger := activity.GetLogger(ctx)

	// Detect source type from extension
	ext := strings.ToLower(filepath.Ext(input.AudioPath))
	isVideo := ext == ".mp4" || ext == ".mkv" || ext == ".mov" || ext == ".webm" || ext == ".avi"
	clipExt := ".wav"
	if isVideo {
		clipExt = ".mp4"
	}

	logger.Info("Extracting clips", "highlight_count", len(input.Highlights), "padding_ms", input.PaddingMs, "source_type", ext, "clip_ext", clipExt)

	if err := os.MkdirAll(input.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	clips := make([]Highlight, 0, len(input.Highlights))
	for i, h := range input.Highlights {
		// Apply padding
		startMs := h.StartMs - int64(input.PaddingMs)
		if startMs < 0 {
			startMs = 0
		}
		endMs := h.EndMs + int64(input.PaddingMs)

		clipPath := filepath.Join(input.OutputDir, fmt.Sprintf("clip-%02d-%s%s", i+1, h.ID, clipExt))

		logger.Info("Extracting clip", "index", i+1, "title", h.Title, "start_ms", startMs, "end_ms", endMs)
		activity.RecordHeartbeat(ctx, fmt.Sprintf("extracting clip %d/%d: %s", i+1, len(input.Highlights), h.Title))

		var err error
		if isVideo {
			err = ExtractClip(ctx, input.AudioPath, clipPath, startMs, endMs)
		} else {
			err = ExtractAudioClip(ctx, input.AudioPath, clipPath, startMs, endMs)
		}
		if err != nil {
			logger.Warn("Failed to extract clip", "title", h.Title, "error", err)
			continue
		}

		clip := h
		clip.ClipPath = clipPath
		clips = append(clips, clip)
	}

	logger.Info("Clips extracted", "success", len(clips), "total", len(input.Highlights))
	return &ExtractClipsResult{Clips: clips}, nil
}
