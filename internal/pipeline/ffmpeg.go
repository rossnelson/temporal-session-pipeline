package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// SilenceInterval represents a detected silence period in an audio file.
type SilenceInterval struct {
	StartSeconds float64
	EndSeconds   float64
}

// ConcatWAVsReencode concatenates input WAVs to a single 16kHz mono 16-bit PCM WAV.
// Unlike ConcatClips (which works on video), this re-encodes audio to a uniform
// format guaranteeing consistent sample rate/channels regardless of input variation.
// Uses ffmpeg's concat filter (not the concat demuxer) so inputs with different
// sample rates/channel counts are normalized on the fly.
func ConcatWAVsReencode(ctx context.Context, inputs []string, output string) error {
	if len(inputs) == 0 {
		return fmt.Errorf("ConcatWAVsReencode: no input files")
	}

	filterParts := make([]string, 0, len(inputs))
	for i := range inputs {
		filterParts = append(filterParts, fmt.Sprintf("[%d:a]", i))
	}
	filter := fmt.Sprintf("%sconcat=n=%d:v=0:a=1[out]", strings.Join(filterParts, ""), len(inputs))

	args := []string{"-hide_banner", "-loglevel", "error"}
	for _, in := range inputs {
		args = append(args, "-i", in)
	}
	args = append(args, "-filter_complex", filter, "-map", "[out]", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-y", output)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg concat WAVs reencode: %w\noutput: %s", err, string(out))
	}
	return nil
}

var (
	reSilenceStart = regexp.MustCompile(`silence_start:\s*([\d.]+)`)
	reSilenceEnd   = regexp.MustCompile(`silence_end:\s*([\d.]+)`)
)

// DetectSilences runs ffmpeg silencedetect on the input and parses the output
// into []SilenceInterval.
func DetectSilences(ctx context.Context, path string, minSilenceMs int, thresholdDB float64) ([]SilenceInterval, error) {
	noiseArg := fmt.Sprintf("silencedetect=noise=%.1fdB:d=%.3f", thresholdDB, float64(minSilenceMs)/1000.0)
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner",
		"-i", path,
		"-af", noiseArg,
		"-f", "null",
		"-",
	)
	// silencedetect writes to stderr
	out, _ := cmd.CombinedOutput()
	output := string(out)

	var intervals []SilenceInterval
	var current *SilenceInterval

	for _, line := range strings.Split(output, "\n") {
		if m := reSilenceStart.FindStringSubmatch(line); m != nil {
			t, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				continue
			}
			current = &SilenceInterval{StartSeconds: t}
		} else if m := reSilenceEnd.FindStringSubmatch(line); m != nil {
			if current == nil {
				continue
			}
			t, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				continue
			}
			current.EndSeconds = t
			intervals = append(intervals, *current)
			current = nil
		}
	}

	return intervals, nil
}

// CutAudioRange extracts [startSec, endSec) from an input WAV into a new WAV.
// For PCM WAV inputs (normalized via ConcatWAVsReencode), -c copy works cleanly.
func CutAudioRange(ctx context.Context, input, output string, startSec, endSec float64) error {
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-ss", fmt.Sprintf("%.3f", startSec),
		"-to", fmt.Sprintf("%.3f", endSec),
		"-i", input,
		"-c", "copy",
		"-y", output,
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg cut audio range %.3f-%.3f: %w\noutput: %s", startSec, endSec, err, string(out))
	}
	return nil
}

// ExtractAudio uses FFmpeg to extract a mono 16kHz WAV from a video file.
func ExtractAudio(ctx context.Context, inputPath, outputPath string) error {
	args := []string{
		"-i", inputPath,
		"-vn",                  // no video
		"-acodec", "pcm_s16le", // 16-bit PCM
		"-ar", "16000",         // 16kHz sample rate
		"-ac", "1",             // mono
		"-y",                   // overwrite
		outputPath,
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg extract audio: %w\noutput: %s", err, string(out))
	}
	return nil
}

// GetDuration returns the duration of a media file in seconds using ffprobe.
func GetDuration(ctx context.Context, filePath string) (float64, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe duration: %w", err)
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// ExtractAudioClip cuts an audio clip at the specified timestamps.
func ExtractAudioClip(ctx context.Context, inputPath, outputPath string, startMs, endMs int64) error {
	startSec := float64(startMs) / 1000.0
	endSec := float64(endMs) / 1000.0

	args := []string{
		"-ss", fmt.Sprintf("%.3f", startSec),
		"-to", fmt.Sprintf("%.3f", endSec),
		"-i", inputPath,
		"-acodec", "pcm_s16le",
		"-y",
		outputPath,
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg extract audio clip: %w\noutput: %s", err, string(out))
	}
	return nil
}

// ExtractClip cuts a video clip at the specified timestamps with optional padding.
func ExtractClip(ctx context.Context, inputPath, outputPath string, startMs, endMs int64) error {
	startSec := float64(startMs) / 1000.0
	endSec := float64(endMs) / 1000.0

	args := []string{
		"-ss", fmt.Sprintf("%.3f", startSec),
		"-to", fmt.Sprintf("%.3f", endSec),
		"-i", inputPath,
		"-c:v", "libx264",
		"-c:a", "aac",
		"-y",
		outputPath,
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg extract clip: %w\noutput: %s", err, string(out))
	}
	return nil
}

// ConcatClips concatenates a list of video clips into a single file using the concat demuxer.
func ConcatClips(ctx context.Context, clipPaths []string, outputPath, concatListPath string) error {
	// Write concat list file
	var sb strings.Builder
	for _, p := range clipPaths {
		sb.WriteString(fmt.Sprintf("file '%s'\n", p))
	}
	if err := os.WriteFile(concatListPath, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("write concat list: %w", err)
	}

	args := []string{
		"-f", "concat",
		"-safe", "0",
		"-i", concatListPath,
		"-c:v", "libx264",
		"-c:a", "aac",
		"-y",
		outputPath,
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg concat: %w\noutput: %s", err, string(out))
	}
	return nil
}
