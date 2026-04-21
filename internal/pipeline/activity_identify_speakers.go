package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"go.temporal.io/sdk/activity"
)

// ProposeSpeakerMappingsActivity computes speaker statistics from the transcript,
// loads existing voice profiles, and proposes speaker-to-player mappings with
// confidence scores.
func ProposeSpeakerMappingsActivity(ctx context.Context, input ProposeMappingsInput) (*ProposeMappingsResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Proposing speaker mappings", "transcript", input.TranscriptPath)

	// Load transcript.
	transcript, err := LoadTranscript(input.TranscriptPath)
	if err != nil {
		return nil, fmt.Errorf("load transcript: %w", err)
	}

	// Compute per-speaker stats from segments.
	stats := ComputeSpeakerStats(transcript.Segments)
	logger.Info("Computed speaker stats", "speakers", len(stats))

	// Load existing voice profiles for pattern matching.
	dataDir := os.Getenv("PIPELINE_DATA_DIR")
	if dataDir == "" {
		dataDir = "/data"
	}
	profileDir := filepath.Join(dataDir, "voice-profiles")
	profiles, err := LoadVoiceProfiles(profileDir)
	if err != nil {
		logger.Warn("No existing voice profiles, will use heuristics only", "error", err)
		profiles = make(map[string]*VoiceProfile)
	} else {
		logger.Info("Loaded voice profiles", "count", len(profiles))
	}

	// Match speakers to known players.
	mappings := MatchSpeakers(stats, profiles, input.KnownPlayers)

	// Compute overall confidence as the average across all mappings.
	totalConf := 0.0
	for _, m := range mappings {
		totalConf += m.Confidence
	}
	n := len(mappings)
	if n == 0 {
		n = 1
	}
	avgConf := totalConf / float64(n)

	// Build reasoning summary.
	reasoning := fmt.Sprintf("Matched %d speakers from %d segments using %d voice profiles. Average confidence: %.2f",
		len(mappings), len(transcript.Segments), len(profiles), avgConf)

	logger.Info("Speaker mapping proposed",
		"mappings", len(mappings),
		"avg_confidence", avgConf,
	)

	return &ProposeMappingsResult{
		Mappings:   mappings,
		Confidence: avgConf,
		Reasoning:  reasoning,
	}, nil
}

// ApplySpeakerMappingsActivity applies confirmed speaker mappings to the
// transcript, setting ResolvedSpeaker on each segment, and updates voice
// profiles with new session data.
func ApplySpeakerMappingsActivity(ctx context.Context, input ApplyMappingsInput) (*ApplyMappingsResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Applying speaker mappings", "transcript", input.TranscriptPath, "mappings", len(input.Mappings))

	// Load transcript.
	transcript, err := LoadTranscript(input.TranscriptPath)
	if err != nil {
		return nil, fmt.Errorf("load transcript: %w", err)
	}

	// Build label -> player mapping for fast lookup.
	labelToPlayer := make(map[string]SpeakerMatch, len(input.Mappings))
	for _, m := range input.Mappings {
		labelToPlayer[m.SpeakerLabel] = m
	}

	// Apply mappings to each segment.
	mappedCount := 0
	for i := range transcript.Segments {
		seg := &transcript.Segments[i]
		if match, ok := labelToPlayer[seg.Speaker]; ok {
			seg.ResolvedSpeaker = match.PlayerName
			mappedCount++
		}
	}

	// Determine output path.
	outputPath := input.OutputPath
	if outputPath == "" {
		outputPath = input.TranscriptPath
	}

	// Save updated transcript.
	if err := SaveTranscript(outputPath, transcript); err != nil {
		return nil, fmt.Errorf("save mapped transcript: %w", err)
	}

	// Update voice profiles with data from this session.
	stats := ComputeSpeakerStats(transcript.Segments)
	dataDir := os.Getenv("PIPELINE_DATA_DIR")
	if dataDir == "" {
		dataDir = "/data"
	}
	profileDir := filepath.Join(dataDir, "voice-profiles")
	profiles, _ := LoadVoiceProfiles(profileDir)
	if profiles == nil {
		profiles = make(map[string]*VoiceProfile)
	}

	for _, m := range input.Mappings {
		if m.PlayerName == "Unknown" {
			continue
		}
		speakerStats, ok := stats[m.SpeakerLabel]
		if !ok {
			continue
		}

		profile, exists := profiles[m.PlayerName]
		if !exists {
			profile = &VoiceProfile{
				PlayerName:    m.PlayerName,
				CharacterName: m.Character,
			}
		}
		profile = UpdateVoiceProfile(profile, speakerStats)

		if err := SaveVoiceProfile(profileDir, profile); err != nil {
			logger.Warn("Failed to save voice profile", "player", m.PlayerName, "error", err)
		}
	}

	logger.Info("Speaker mappings applied",
		"mapped_segments", mappedCount,
		"total_segments", len(transcript.Segments),
		"output", outputPath,
	)

	return &ApplyMappingsResult{
		MappedTranscriptPath: outputPath,
		MappedSpeakers:       len(input.Mappings),
	}, nil
}
