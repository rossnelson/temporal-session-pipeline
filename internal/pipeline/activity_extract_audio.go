package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"go.temporal.io/sdk/activity"
)

// ExtractAudioActivity extracts audio from a video recording as a mono 16kHz WAV.
func ExtractAudioActivity(ctx context.Context, input ExtractAudioInput) (*ExtractAudioResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Extracting audio", "recording", input.RecordingPath, "output", input.OutputPath)

	if err := os.MkdirAll(filepath.Dir(input.OutputPath), 0755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	if err := ExtractAudio(ctx, input.RecordingPath, input.OutputPath); err != nil {
		return nil, fmt.Errorf("extract audio: %w", err)
	}

	duration, err := GetDuration(ctx, input.OutputPath)
	if err != nil {
		logger.Warn("Could not get duration", "error", err)
	}

	logger.Info("Audio extracted", "output", input.OutputPath, "duration_seconds", duration)
	return &ExtractAudioResult{
		AudioPath:       input.OutputPath,
		DurationSeconds: duration,
	}, nil
}
