package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"go.temporal.io/sdk/activity"
)

// AssembleReelActivity concatenates extracted clips into a single highlight reel.
func AssembleReelActivity(ctx context.Context, input AssembleReelInput) (*AssembleReelResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Assembling highlight reel", "clip_count", len(input.Clips))

	if len(input.Clips) == 0 {
		return &AssembleReelResult{ReelPath: "", DurationSeconds: 0}, nil
	}

	// Collect clip paths
	clipPaths := make([]string, 0, len(input.Clips))
	for _, c := range input.Clips {
		if c.ClipPath != "" {
			clipPaths = append(clipPaths, c.ClipPath)
		}
	}

	if len(clipPaths) == 0 {
		return &AssembleReelResult{ReelPath: "", DurationSeconds: 0}, nil
	}

	// Write concat list
	concatListPath := filepath.Join(filepath.Dir(input.OutputPath), "concat-list.txt")

	if err := ConcatClips(ctx, clipPaths, input.OutputPath, concatListPath); err != nil {
		return nil, fmt.Errorf("assemble reel: %w", err)
	}

	// Clean up concat list
	_ = os.Remove(concatListPath)

	duration, _ := GetDuration(ctx, input.OutputPath)

	logger.Info("Highlight reel assembled", "path", input.OutputPath, "duration", duration)
	return &AssembleReelResult{
		ReelPath:        input.OutputPath,
		DurationSeconds: duration,
	}, nil
}
