package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"go.temporal.io/sdk/activity"
)

// MergeTranscriptsActivity concatenates per-chunk transcript JSONs into a
// single session transcript written to input.OutputPath.
//
// Because transcribe.py was invoked with --start-offset-seconds per chunk,
// each chunk's segments already carry session-absolute start_ms/end_ms, so
// this merge is a pure append — no per-segment offset arithmetic is done
// here (matches decision Q4 in the plan: explicit offsets end-to-end, no
// Go-side drift accumulation).
func MergeTranscriptsActivity(ctx context.Context, input MergeTranscriptsInput) (*MergeTranscriptsResult, error) {
	logger := activity.GetLogger(ctx)

	if len(input.ChunkResults) == 0 {
		return nil, fmt.Errorf("MergeTranscriptsActivity: no chunk results provided")
	}

	// Defensive sort — workflow builds the slice in-order but replay/retry
	// semantics make it cheap insurance.
	chunks := make([]DiarizeResult, len(input.ChunkResults))
	copy(chunks, input.ChunkResults)
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].ChunkIndex < chunks[j].ChunkIndex
	})

	// Load each per-chunk transcript.
	loaded := make([]*SessionTranscript, 0, len(chunks))
	chunkMeta := make([]map[string]interface{}, 0, len(chunks))
	var sumDurationSeconds float64

	for i, r := range chunks {
		t, err := LoadTranscript(r.TranscriptPath)
		if err != nil {
			return nil, fmt.Errorf("load chunk %d transcript %q: %w", r.ChunkIndex, r.TranscriptPath, err)
		}
		loaded = append(loaded, t)
		sumDurationSeconds += t.DurationSeconds
		chunkMeta = append(chunkMeta, map[string]interface{}{
			"index":                r.ChunkIndex,
			"start_offset_seconds": r.StartOffsetSeconds,
			"duration_seconds":     t.DurationSeconds,
			"path":                 r.TranscriptPath,
		})
		activity.RecordHeartbeat(ctx, fmt.Sprintf("merge: loaded chunk %d/%d", i+1, len(chunks)))
	}

	// Boundary sanity check across adjacent chunks — warn on overlap but do
	// not fail: pyannote can misalign by <1s near boundaries. The timestamps
	// are session-absolute (Python added --start-offset-seconds before writing).
	for i := 0; i < len(loaded)-1; i++ {
		prev := loaded[i]
		next := loaded[i+1]
		if len(prev.Segments) == 0 || len(next.Segments) == 0 {
			continue
		}
		prevLastStart := prev.Segments[len(prev.Segments)-1].StartMs
		nextFirstStart := next.Segments[0].StartMs
		if nextFirstStart < prevLastStart {
			logger.Warn("chunk boundary overlap detected",
				"prev_chunk_index", chunks[i].ChunkIndex,
				"next_chunk_index", chunks[i+1].ChunkIndex,
				"prev_last_start_ms", prevLastStart,
				"next_first_start_ms", nextFirstStart,
				"overlap_ms", prevLastStart-nextFirstStart,
			)
		}
	}

	// Concatenate segments in chunk order (pure append; timestamps are
	// already session-absolute thanks to Python-side offset).
	var totalSegments int
	for _, t := range loaded {
		totalSegments += len(t.Segments)
	}
	mergedSegments := make([]Segment, 0, totalSegments)
	speakerSet := make(map[string]struct{})
	var lastEndMs int64
	for _, t := range loaded {
		mergedSegments = append(mergedSegments, t.Segments...)
		for _, seg := range t.Segments {
			if seg.Speaker != "" {
				speakerSet[seg.Speaker] = struct{}{}
			}
			if seg.EndMs > lastEndMs {
				lastEndMs = seg.EndMs
			}
		}
	}

	speakers := make([]string, 0, len(speakerSet))
	for s := range speakerSet {
		speakers = append(speakers, s)
	}
	sort.Strings(speakers)

	// duration_seconds: max of sum-of-chunk-durations and last segment end_ms.
	durationSeconds := sumDurationSeconds
	if float64(lastEndMs)/1000.0 > durationSeconds {
		durationSeconds = float64(lastEndMs) / 1000.0
	}

	// Pick provider/model from the first chunk — all chunks run through the
	// same TranscribeAudioActivity configuration so they match.
	provider := loaded[0].Provider
	model := loaded[0].Model

	merged := &SessionTranscript{
		SessionID:       input.SessionID,
		RecordingFile:   filepath.Base(input.ConcatAudioPath),
		DurationSeconds: durationSeconds,
		Provider:        provider,
		Model:           model,
		Speakers:        speakers,
		Segments:        mergedSegments,
		Metadata: map[string]interface{}{
			"chunks": chunkMeta,
		},
	}

	if err := SaveTranscript(input.OutputPath, merged); err != nil {
		return nil, fmt.Errorf("save merged transcript: %w", err)
	}
	activity.RecordHeartbeat(ctx, "merge: saved merged transcript")

	logger.Info("Merged transcripts",
		"chunks", len(loaded),
		"segments", len(mergedSegments),
		"speakers", len(speakers),
		"duration_seconds", durationSeconds,
		"output", input.OutputPath,
	)

	return &MergeTranscriptsResult{
		TranscriptPath:  input.OutputPath,
		SpeakerCount:    len(speakers),
		SegmentCount:    len(mergedSegments),
		DurationSeconds: durationSeconds,
	}, nil
}
