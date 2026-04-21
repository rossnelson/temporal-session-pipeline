package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"go.temporal.io/sdk/activity"
)

// ffprobeFormat represents the format section of ffprobe JSON output.
type ffprobeFormat struct {
	Tags map[string]string `json:"tags"`
}

type ffprobeOutput struct {
	Format ffprobeFormat `json:"format"`
}

// ExtractSessionDateActivity reads recording metadata to find the origination date.
// Checks BEXT (Broadcast WAV) tags and common metadata fields.
func ExtractSessionDateActivity(ctx context.Context, input ExtractSessionDateInput) (*ExtractSessionDateResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Extracting session date from recording metadata", "recording", input.RecordingPath)

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		input.RecordingPath,
	)
	out, err := cmd.Output()
	if err != nil {
		logger.Warn("ffprobe failed, no date metadata available", "error", err)
		return &ExtractSessionDateResult{Found: false}, nil
	}

	var probe ffprobeOutput
	if err := json.Unmarshal(out, &probe); err != nil {
		logger.Warn("Failed to parse ffprobe output", "error", err)
		return &ExtractSessionDateResult{Found: false}, nil
	}

	// Check tags in priority order for date information
	// BEXT: origination_date (Zoom recorders, broadcast equipment)
	// Common: creation_time, date (MP4, MKV containers)
	dateKeys := []string{
		"origination_date",
		"creation_time",
		"date",
		"DATE_RECORDED",
		"date_recorded",
	}

	for _, key := range dateKeys {
		if val, ok := probe.Format.Tags[key]; ok && val != "" {
			date := normalizeDate(val)
			if date != "" {
				logger.Info("Session date found in metadata", "key", key, "raw", val, "normalized", date)
				return &ExtractSessionDateResult{Date: date, Found: true}, nil
			}
		}
	}

	logger.Info("No date metadata found in recording")
	return &ExtractSessionDateResult{Found: false}, nil
}

// normalizeDate attempts to extract a YYYY-MM-DD date from various formats.
// Handles: "2026-04-12", "2026-04-12T19:30:00Z", "2026:04:12", "2026/04/12", etc.
func normalizeDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) < 10 {
		return ""
	}

	// Try standard formats
	s := raw[:10]
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, "/", "-")

	// Validate it looks like YYYY-MM-DD
	parts := strings.Split(s, "-")
	if len(parts) != 3 || len(parts[0]) != 4 || len(parts[1]) != 2 || len(parts[2]) != 2 {
		return ""
	}

	// Basic sanity check
	if parts[0] < "2020" || parts[0] > "2099" {
		return ""
	}

	return fmt.Sprintf("%s-%s-%s", parts[0], parts[1], parts[2])
}
