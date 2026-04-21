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
	"time"

	"go.temporal.io/sdk/activity"
)

// transcribeProgress represents a progress JSON line emitted by
// transcribe_audio.py or diarize_audio.py. Both scripts share the same
// schema: {type, percent, message, segments, speakers, duration_seconds}.
type transcribeProgress struct {
	Type     string `json:"type"`
	Percent  int    `json:"percent,omitempty"`
	Message  string `json:"message,omitempty"`
	Segments int    `json:"segments,omitempty"`
	Duration int    `json:"duration_seconds,omitempty"`
	Speakers int    `json:"speakers,omitempty"`
}

// TranscribeAudioActivity spawns transcribe_audio.py (Whisper only), reads
// progress JSON lines from stdout, heartbeats back to Temporal, and returns
// the path to the raw transcript JSON.
func TranscribeAudioActivity(ctx context.Context, input TranscribeAudioInput) (*TranscribeAudioResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Starting Whisper transcription",
		"audio", input.AudioPath,
		"model", input.Model,
		"compute_type", input.ComputeType,
		"chunk_index", input.ChunkIndex,
		"start_offset_seconds", input.StartOffsetSeconds,
		"output", input.OutputPath,
	)

	// Resolve script path: /app/scripts/ in Docker, fallback to local dev
	scriptPath := "/app/scripts/transcribe_audio.py"
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		if wd, err := os.Getwd(); err == nil {
			scriptPath = filepath.Join(wd, "scripts", "transcribe_audio.py")
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
		"--model", input.Model,
		"--compute-type", input.ComputeType,
		"--output", input.OutputPath,
	}
	if input.ModelDir != "" {
		args = append(args, "--model-dir", input.ModelDir)
	}
	if input.StartOffsetSeconds != 0.0 {
		args = append(args, "--start-offset-seconds", strconv.FormatFloat(input.StartOffsetSeconds, 'f', -1, 64))
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
		return nil, fmt.Errorf("start transcribe_audio.py: %w", err)
	}

	// Background heartbeat ticker (every 30s)
	heartbeatMsg := fmt.Sprintf("transcribing chunk %d", input.ChunkIndex)
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
		activity.RecordHeartbeat(ctx, fmt.Sprintf("transcription: %d%% - %s", progress.Percent, progress.Message))
		logger.Info("Transcription progress", "percent", progress.Percent, "message", progress.Message)
	}

	close(heartbeatDone)

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("transcribe_audio.py failed: %w", err)
	}

	logger.Info("Whisper transcription complete",
		"chunk_index", input.ChunkIndex,
		"segments", lastProgress.Segments,
		"duration_seconds", lastProgress.Duration,
	)

	// Load the raw transcript from disk to populate result fields
	rawData, err := os.ReadFile(input.OutputPath)
	if err != nil {
		return nil, fmt.Errorf("read raw transcript: %w", err)
	}
	var raw RawTranscript
	if err := json.Unmarshal(rawData, &raw); err != nil {
		return nil, fmt.Errorf("parse raw transcript: %w", err)
	}

	return &TranscribeAudioResult{
		RawTranscriptPath:  input.OutputPath,
		SegmentCount:       len(raw.Segments),
		DurationSeconds:    raw.DurationSeconds,
		ChunkIndex:         input.ChunkIndex,
		StartOffsetSeconds: input.StartOffsetSeconds,
	}, nil
}
