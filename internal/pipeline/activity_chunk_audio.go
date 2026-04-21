package pipeline

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"go.temporal.io/sdk/activity"
)

// ConcatAndChunkAudioActivity concatenates the input recordings into a single
// session WAV and splits it into ~20-minute chunks aligned to silence boundaries.
func ConcatAndChunkAudioActivity(ctx context.Context, input ConcatAndChunkInput) (*ConcatAndChunkResult, error) {
	if len(input.RecordingPaths) == 0 {
		return nil, fmt.Errorf("ConcatAndChunkAudioActivity: no recording paths provided")
	}

	targetChunkSeconds := input.TargetChunkSeconds
	if targetChunkSeconds <= 0 {
		targetChunkSeconds = 1200.0 // 20 minutes
	}
	silenceTolerance := input.SilenceToleranceSeconds
	if silenceTolerance <= 0 {
		silenceTolerance = 60.0
	}

	if err := os.MkdirAll(input.ChunkOutputDir, 0755); err != nil {
		return nil, fmt.Errorf("create chunk output dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(input.ConcatOutputPath), 0755); err != nil {
		return nil, fmt.Errorf("create concat output dir: %w", err)
	}

	// Concatenate (or extract) to a single normalized WAV
	if len(input.RecordingPaths) == 1 {
		if err := ExtractAudio(ctx, input.RecordingPaths[0], input.ConcatOutputPath); err != nil {
			return nil, fmt.Errorf("extract single audio: %w", err)
		}
	} else {
		if err := ConcatWAVsReencode(ctx, input.RecordingPaths, input.ConcatOutputPath); err != nil {
			return nil, fmt.Errorf("concat WAVs: %w", err)
		}
	}

	totalDur, err := GetDuration(ctx, input.ConcatOutputPath)
	if err != nil {
		return nil, fmt.Errorf("get duration of concat audio: %w", err)
	}

	logger := activity.GetLogger(ctx)

	// Short audio: single chunk spanning the whole file
	if totalDur <= targetChunkSeconds {
		chunk := ChunkInfo{
			Index:              0,
			Path:               filepath.Join(input.ChunkOutputDir, "chunk-00.wav"),
			StartOffsetSeconds: 0,
			DurationSeconds:    totalDur,
			LabelPrefix:        fmt.Sprintf("chunk_0%s", ChunkLabelSeparator),
		}
		if err := CutAudioRange(ctx, input.ConcatOutputPath, chunk.Path, 0, totalDur); err != nil {
			return nil, fmt.Errorf("cut single chunk: %w", err)
		}
		return &ConcatAndChunkResult{
			ConcatAudioPath:      input.ConcatOutputPath,
			TotalDurationSeconds: totalDur,
			Chunks:               []ChunkInfo{chunk},
		}, nil
	}

	// Compute ideal boundary times
	var boundaries []float64
	t := targetChunkSeconds
	for t < totalDur {
		boundaries = append(boundaries, t)
		t += targetChunkSeconds
	}

	// Detect silences once over the full concat file
	silences, err := DetectSilences(ctx, input.ConcatOutputPath, 500, -30.0)
	if err != nil {
		return nil, fmt.Errorf("detect silences: %w", err)
	}

	// Snap each boundary to the nearest silence midpoint within tolerance
	snapped := make([]float64, len(boundaries))
	for i, b := range boundaries {
		best := b
		bestDist := silenceTolerance + 1 // start outside tolerance

		for _, si := range silences {
			mid := (si.StartSeconds + si.EndSeconds) / 2.0
			dist := math.Abs(mid - b)
			if dist < bestDist {
				bestDist = dist
				best = mid
			}
		}

		if bestDist > silenceTolerance {
			logger.Warn("no silence found near boundary, using fixed time",
				"boundary_sec", b,
				"tolerance_sec", silenceTolerance,
			)
			best = b
		}

		snapped[i] = best
	}

	// Build chunks from intervals: [0, s0), [s0, s1), ..., [sN, totalDur]
	allBounds := append([]float64{0}, snapped...)
	allBounds = append(allBounds, totalDur)

	chunks := make([]ChunkInfo, 0, len(allBounds)-1)
	for i := 0; i < len(allBounds)-1; i++ {
		startSec := allBounds[i]
		endSec := allBounds[i+1]
		chunkPath := filepath.Join(input.ChunkOutputDir, fmt.Sprintf("chunk-%02d.wav", i))

		if err := CutAudioRange(ctx, input.ConcatOutputPath, chunkPath, startSec, endSec); err != nil {
			return nil, fmt.Errorf("cut chunk %d: %w", i, err)
		}

		chunks = append(chunks, ChunkInfo{
			Index:              i,
			Path:               chunkPath,
			StartOffsetSeconds: startSec,
			DurationSeconds:    endSec - startSec,
			LabelPrefix:        fmt.Sprintf("chunk_%d%s", i, ChunkLabelSeparator),
		})

		activity.RecordHeartbeat(ctx, fmt.Sprintf("chunk %d/%d cut", i+1, len(allBounds)-1))
	}

	return &ConcatAndChunkResult{
		ConcatAudioPath:      input.ConcatOutputPath,
		TotalDurationSeconds: totalDur,
		Chunks:               chunks,
	}, nil
}
