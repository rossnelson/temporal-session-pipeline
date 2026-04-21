package pipeline

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// buildPartySection returns a markdown block listing each known player,
// their character, and their class so Claude attributes voice/action to the right character.
func buildPartySection() string {
	var sb strings.Builder
	sb.WriteString("## Party Cast (ground truth — use these exact names and classes)\n\n")
	for _, p := range KnownPlayers {
		if p.Role == "dm" {
			sb.WriteString(fmt.Sprintf("- **%s** — Dungeon Master (no PC)\n", p.Name))
			continue
		}
		line := fmt.Sprintf("- **%s** plays **%s**", p.Name, p.Character)
		if p.Class != "" {
			line += fmt.Sprintf(" (%s)", p.Class)
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\nAttribute character actions and class-specific abilities (spells, features, fighting styles) to the CORRECT character based on this cast list. Do not infer class from dialogue patterns.\n\n")
	return sb.String()
}

// SpeakerStats holds computed speaking statistics for a single speaker label.
type SpeakerStats struct {
	Label             string
	WordCount         int
	TurnCount         int
	TotalDurationMs   int64
	SpeakingRatio     float64
	AvgWordsPerTurn   float64
	AvgTurnDurationMs float64
}

// LoadTranscript reads a SessionTranscript from a JSON file.
func LoadTranscript(path string) (*SessionTranscript, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read transcript file: %w", err)
	}
	var t SessionTranscript
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("unmarshal transcript: %w", err)
	}
	return &t, nil
}

// SaveTranscript writes a SessionTranscript to a JSON file.
func SaveTranscript(path string, t *SessionTranscript) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal transcript: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create transcript dir: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// LoadVoiceProfiles loads all voice profile JSON files from a directory.
func LoadVoiceProfiles(dir string) (map[string]*VoiceProfile, error) {
	profiles := make(map[string]*VoiceProfile)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read profile dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var p VoiceProfile
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		profiles[p.PlayerName] = &p
	}

	return profiles, nil
}

// SaveVoiceProfile writes a single voice profile as JSON.
func SaveVoiceProfile(dir string, profile *VoiceProfile) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create profile dir: %w", err)
	}
	filename := strings.ToLower(strings.ReplaceAll(profile.PlayerName, " ", "-")) + ".json"
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal profile: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, filename), data, 0644)
}

// ComputeSpeakerStats computes per-speaker statistics from transcript segments.
func ComputeSpeakerStats(segments []Segment) map[string]*SpeakerStats {
	stats := make(map[string]*SpeakerStats)

	// Total speaking duration across all speakers for ratio calculation.
	var totalDurationMs int64

	for _, seg := range segments {
		label := seg.Speaker
		s, ok := stats[label]
		if !ok {
			s = &SpeakerStats{Label: label}
			stats[label] = s
		}
		words := len(strings.Fields(seg.Text))
		dur := seg.EndMs - seg.StartMs

		s.WordCount += words
		s.TurnCount++
		s.TotalDurationMs += dur
		totalDurationMs += dur
	}

	// Compute derived stats.
	for _, s := range stats {
		if s.TurnCount > 0 {
			s.AvgWordsPerTurn = float64(s.WordCount) / float64(s.TurnCount)
			s.AvgTurnDurationMs = float64(s.TotalDurationMs) / float64(s.TurnCount)
		}
		if totalDurationMs > 0 {
			s.SpeakingRatio = float64(s.TotalDurationMs) / float64(totalDurationMs)
		}
	}

	return stats
}

// MatchSpeakers matches speaker labels to known players using speaking pattern
// heuristics and stored voice profiles.
func MatchSpeakers(stats map[string]*SpeakerStats, profiles map[string]*VoiceProfile, players []Player) []SpeakerMatch {
	// Sort speakers by speaking ratio descending.
	sorted := make([]*SpeakerStats, 0, len(stats))
	for _, s := range stats {
		sorted = append(sorted, s)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].SpeakingRatio > sorted[j].SpeakingRatio
	})

	// Separate DM and non-DM players.
	var dmPlayer *Player
	nonDMPlayers := make([]Player, 0, len(players))
	for i := range players {
		if players[i].Role == "dm" {
			dmPlayer = &players[i]
		} else {
			nonDMPlayers = append(nonDMPlayers, players[i])
		}
	}

	matches := make([]SpeakerMatch, 0, len(sorted))
	usedPlayers := make(map[string]bool)

	// Step 1: Assign DM to the speaker with the highest speaking ratio.
	if dmPlayer != nil && len(sorted) > 0 {
		dm := sorted[0]
		conf := dmConfidence(dm.SpeakingRatio)
		matches = append(matches, SpeakerMatch{
			SpeakerLabel: dm.Label,
			PlayerName:   dmPlayer.Name,
			Character:    dmPlayer.Character,
			Confidence:   conf,
		})
		usedPlayers[dmPlayer.Name] = true
	}

	// Step 2: Match remaining speakers to non-DM players.
	remainingSpeakers := sorted
	if dmPlayer != nil && len(sorted) > 0 {
		remainingSpeakers = sorted[1:]
	}

	if len(profiles) > 0 {
		// Profile-based matching: compute similarity scores.
		matches = append(matches, matchWithProfiles(remainingSpeakers, profiles, nonDMPlayers, usedPlayers)...)
	} else {
		// No profiles: assign by speaking ratio order, low confidence.
		matches = append(matches, matchByOrder(remainingSpeakers, nonDMPlayers, usedPlayers)...)
	}

	return matches
}

// dmConfidence returns confidence that the top speaker is the DM based on
// speaking ratio. DMs typically have 35-55% of total speaking time.
func dmConfidence(ratio float64) float64 {
	if ratio >= 0.35 && ratio <= 0.55 {
		return 0.95
	}
	if ratio >= 0.25 && ratio < 0.35 {
		return 0.75
	}
	if ratio > 0.55 && ratio <= 0.65 {
		return 0.80
	}
	return 0.50
}

// matchWithProfiles matches speakers to players using stored voice profiles.
func matchWithProfiles(speakers []*SpeakerStats, profiles map[string]*VoiceProfile, players []Player, used map[string]bool) []SpeakerMatch {
	var matches []SpeakerMatch

	// Build a score matrix: speaker x player.
	type scored struct {
		speakerIdx int
		playerIdx  int
		score      float64
	}
	var scores []scored

	for si, sp := range speakers {
		for pi, pl := range players {
			if used[pl.Name] {
				continue
			}
			prof, ok := profiles[pl.Name]
			if !ok {
				// No profile for this player; assign a low baseline score.
				scores = append(scores, scored{si, pi, 0.30})
				continue
			}
			sim := patternSimilarity(sp, &prof.SpeakingPatterns)
			scores = append(scores, scored{si, pi, sim})
		}
	}

	// Greedy assignment: pick highest score, assign, remove both from pool.
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	assignedSpeakers := make(map[int]bool)
	assignedPlayers := make(map[int]bool)

	for _, sc := range scores {
		if assignedSpeakers[sc.speakerIdx] || assignedPlayers[sc.playerIdx] {
			continue
		}
		sp := speakers[sc.speakerIdx]
		pl := players[sc.playerIdx]
		matches = append(matches, SpeakerMatch{
			SpeakerLabel: sp.Label,
			PlayerName:   pl.Name,
			Character:    pl.Character,
			Confidence:   sc.score,
		})
		assignedSpeakers[sc.speakerIdx] = true
		assignedPlayers[sc.playerIdx] = true
		used[pl.Name] = true
	}

	// Any unmatched speakers get "Unknown" entries.
	for i, sp := range speakers {
		if !assignedSpeakers[i] {
			matches = append(matches, SpeakerMatch{
				SpeakerLabel: sp.Label,
				PlayerName:   "Unknown",
				Character:    "Unknown",
				Confidence:   0.0,
			})
		}
	}

	return matches
}

// matchByOrder assigns speakers to players by speaking ratio order when no
// profiles exist. Confidence is low since this is purely heuristic.
func matchByOrder(speakers []*SpeakerStats, players []Player, used map[string]bool) []SpeakerMatch {
	var matches []SpeakerMatch

	pi := 0
	for _, sp := range speakers {
		if pi < len(players) {
			pl := players[pi]
			if used[pl.Name] {
				pi++
				if pi >= len(players) {
					matches = append(matches, SpeakerMatch{
						SpeakerLabel: sp.Label,
						PlayerName:   "Unknown",
						Character:    "Unknown",
						Confidence:   0.0,
					})
					continue
				}
				pl = players[pi]
			}
			matches = append(matches, SpeakerMatch{
				SpeakerLabel: sp.Label,
				PlayerName:   pl.Name,
				Character:    pl.Character,
				Confidence:   0.40, // Low confidence without profiles.
			})
			used[pl.Name] = true
			pi++
		} else {
			matches = append(matches, SpeakerMatch{
				SpeakerLabel: sp.Label,
				PlayerName:   "Unknown",
				Character:    "Unknown",
				Confidence:   0.0,
			})
		}
	}

	return matches
}

// patternSimilarity computes a 0-1 similarity score between observed speaker
// stats and a stored voice profile's speaking patterns.
func patternSimilarity(stats *SpeakerStats, patterns *SpeakingPatterns) float64 {
	// Weight each feature equally.
	ratioSim := 1.0 - math.Min(1.0, math.Abs(stats.SpeakingRatio-patterns.SpeakingRatio)/0.3)
	wptSim := 1.0 - math.Min(1.0, math.Abs(stats.AvgWordsPerTurn-patterns.AvgWordsPerTurn)/50.0)
	durSim := 1.0 - math.Min(1.0, math.Abs(stats.AvgTurnDurationMs-patterns.AvgTurnDurationMs)/10000.0)

	combined := (ratioSim*0.4 + wptSim*0.3 + durSim*0.3)

	// Clamp to [0, 1].
	if combined < 0 {
		return 0
	}
	if combined > 1 {
		return 1
	}
	return combined
}

// UpdateVoiceProfile updates a voice profile with new session data using an
// exponential moving average to weight recent sessions more heavily.
func UpdateVoiceProfile(profile *VoiceProfile, stats *SpeakerStats) *VoiceProfile {
	if profile.SessionsCount == 0 {
		// First session: initialize from stats.
		profile.SpeakingPatterns = SpeakingPatterns{
			AvgWordsPerTurn:   stats.AvgWordsPerTurn,
			AvgTurnDurationMs: stats.AvgTurnDurationMs,
			SpeakingRatio:     stats.SpeakingRatio,
		}
		profile.SessionsCount = 1
		profile.LastUpdated = time.Now()
		return profile
	}

	// Exponential moving average with alpha = 0.3 for recent session weighting.
	const alpha = 0.3
	p := &profile.SpeakingPatterns
	p.AvgWordsPerTurn = ema(p.AvgWordsPerTurn, stats.AvgWordsPerTurn, alpha)
	p.AvgTurnDurationMs = ema(p.AvgTurnDurationMs, stats.AvgTurnDurationMs, alpha)
	p.SpeakingRatio = ema(p.SpeakingRatio, stats.SpeakingRatio, alpha)

	profile.SessionsCount++
	profile.LastUpdated = time.Now()
	return profile
}

// ema computes an exponential moving average step.
func ema(old, new, alpha float64) float64 {
	return old*(1-alpha) + new*alpha
}
