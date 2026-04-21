package pipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
)

// DiarizeAudioActivity spawns diarize_audio.py (pyannote + speaker assignment),
// reads progress JSON lines from stdout, heartbeats back to Temporal, and
// verifies the output transcript has at least 2 distinct speakers.
func DiarizeAudioActivity(ctx context.Context, input DiarizeInput) (*DiarizeResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Starting speaker diarization",
		"audio", input.AudioPath,
		"raw_transcript", input.RawTranscriptPath,
		"output", input.OutputPath,
		"chunk_index", input.ChunkIndex,
		"min_speakers", input.MinSpeakers,
		"max_speakers", input.MaxSpeakers,
		"start_offset_seconds", input.StartOffsetSeconds,
	)

	// Resolve script path: /app/scripts/ in Docker, fallback to local dev
	scriptPath := "/app/scripts/diarize_audio.py"
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		if wd, err := os.Getwd(); err == nil {
			scriptPath = filepath.Join(wd, "scripts", "diarize_audio.py")
		}
	}

	// Resolve Python: prefer PYTHON_PATH env, then python3 on PATH
	pythonPath := os.Getenv("PYTHON_PATH")
	if pythonPath == "" {
		pythonPath = "python3"
	}

	args := []string{
		scriptPath,
		"--audio", input.AudioPath,
		"--raw-transcript", input.RawTranscriptPath,
		"--output", input.OutputPath,
		"--min-speakers", strconv.Itoa(input.MinSpeakers),
		"--max-speakers", strconv.Itoa(input.MaxSpeakers),
	}
	if input.LabelPrefix != "" {
		args = append(args, "--label-prefix", input.LabelPrefix)
	}
	if input.ComputeType != "" {
		args = append(args, "--compute-type", input.ComputeType)
	}

	if err := os.MkdirAll(filepath.Dir(input.OutputPath), 0755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	cmd := exec.CommandContext(ctx, pythonPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start diarize_audio.py: %w", err)
	}

	// Background heartbeat ticker (every 30s)
	heartbeatMsg := fmt.Sprintf("diarizing chunk %d", input.ChunkIndex)
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				activity.RecordHeartbeat(ctx, heartbeatMsg)
			case <-heartbeatDone:
				return
			}
		}
	}()

	// Parse progress from stdout
	var lastProgress transcribeProgress
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		var progress transcribeProgress
		if err := json.Unmarshal(scanner.Bytes(), &progress); err != nil {
			logger.Warn("Unparseable progress line", "line", scanner.Text())
			continue
		}
		lastProgress = progress
		activity.RecordHeartbeat(ctx, fmt.Sprintf("diarization: %d%% - %s", progress.Percent, progress.Message))
		logger.Info("Diarization progress", "percent", progress.Percent, "message", progress.Message)
	}

	close(heartbeatDone)

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("diarize_audio.py failed: %w", err)
	}

	logger.Info("Diarization complete",
		"chunk_index", input.ChunkIndex,
		"segments", lastProgress.Segments,
		"speakers", lastProgress.Speakers,
	)

	// After subprocess success, verify the output has sane speaker count
	if err := verifyTranscriptQuality(input.OutputPath); err != nil {
		return nil, fmt.Errorf("transcript verification failed: %w", err)
	}

	return &DiarizeResult{
		TranscriptPath:     input.OutputPath,
		SpeakerCount:       lastProgress.Speakers,
		SegmentCount:       lastProgress.Segments,
		ChunkIndex:         input.ChunkIndex,
		StartOffsetSeconds: input.StartOffsetSeconds,
	}, nil
}

// verifyTranscriptQuality reads the final transcript JSON and checks that
// diarization produced more than one speaker. Returns an error if the
// transcript looks like a silent-fallback result. This is a belt-and-suspenders
// check against regressions in the Python fail-fast paths.
func verifyTranscriptQuality(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read transcript: %w", err)
	}
	var tr struct {
		Segments []struct {
			Speaker string `json:"speaker"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(data, &tr); err != nil {
		return fmt.Errorf("parse transcript: %w", err)
	}
	if len(tr.Segments) == 0 {
		return fmt.Errorf("transcript has zero segments")
	}
	distinct := make(map[string]struct{})
	for _, seg := range tr.Segments {
		label := seg.Speaker
		if idx := strings.LastIndex(label, ":"); idx >= 0 {
			label = label[idx+1:]
		}
		distinct[label] = struct{}{}
	}
	if len(distinct) < 2 {
		return fmt.Errorf("transcript has only %d distinct speaker label(s); expected at least 2 for a multi-person session (possible silent diarization fallback)", len(distinct))
	}
	return nil
}
