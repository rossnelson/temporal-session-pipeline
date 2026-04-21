#!/usr/bin/env python3
"""Whisper-only transcription — produces raw segments (text + timestamps, NO speaker labels).

Output is a raw transcript JSON (schema_version: raw-1) with session-absolute timestamps.
Feed the output to diarize_audio.py to assign speaker labels.
"""
import argparse
import json
import os
import sys


def progress(pct, msg):
    """Print a JSON progress line to stdout."""
    print(json.dumps({"type": "progress", "percent": pct, "message": msg}), flush=True)


def main():
    parser = argparse.ArgumentParser(
        description="Transcribe audio with faster-whisper (Whisper only, no diarization)"
    )
    parser.add_argument("--audio", required=True, help="Path to chunk WAV file")
    parser.add_argument("--model", default="small", help="Whisper model name")
    parser.add_argument("--compute-type", default="int8", help="Compute type for CTranslate2")
    parser.add_argument("--output", required=True, help="Path to write raw transcript JSON")
    parser.add_argument("--model-dir", default=None, help="Directory for cached faster-whisper models")
    parser.add_argument(
        "--start-offset-seconds",
        type=float,
        default=0.0,
        help=(
            "Seconds to add to every segment timestamp so they are session-absolute. "
            "Pass the chunk's start position in the full session (e.g. 1200.0 for chunk 1 "
            "that starts 20 min into the recording). Default 0."
        ),
    )
    args = parser.parse_args()

    # Step 1: Load model
    progress(5, "Loading faster-whisper model...")
    from faster_whisper import WhisperModel

    model_kwargs = {
        "model_size_or_path": args.model,
        "device": "cpu",
        "compute_type": args.compute_type,
    }
    if args.model_dir:
        model_kwargs["download_root"] = args.model_dir

    model = WhisperModel(**model_kwargs)
    progress(10, "Model loaded")

    # Step 2: Transcribe
    progress(15, "Starting transcription...")
    segments_gen, info = model.transcribe(
        args.audio,
        beam_size=5,
        language="en",
        vad_filter=True,
        vad_parameters=dict(min_silence_duration_ms=500),
    )

    # Pre-compute offset in ms once so we don't recompute per segment.
    # All start_ms / end_ms in the output are SESSION-ABSOLUTE:
    #   start_ms = int(seg.start * 1000) + offset_ms
    # diarize_audio.py receives these absolute values and must NOT re-apply the offset.
    # It DOES need to convert pyannote's chunk-local timestamps to absolute (see §2.1, §8.4).
    offset_ms = int(args.start_offset_seconds * 1000)

    raw_segments = []
    total_duration = info.duration
    for seg in segments_gen:
        raw_segments.append({
            "start_ms": int(seg.start * 1000) + offset_ms,
            "end_ms": int(seg.end * 1000) + offset_ms,
            "text": seg.text.strip(),
        })
        if total_duration > 0:
            pct = min(90, int(15 + (seg.end / total_duration) * 75))
            progress(pct, f"Transcribing... ({len(raw_segments)} segments)")

    progress(92, f"Transcription complete: {len(raw_segments)} segments")

    # Step 3: Build and write raw transcript JSON
    word_count = sum(len(s["text"].split()) for s in raw_segments)
    recording_file = os.path.basename(args.audio)

    raw_transcript = {
        "schema_version": "raw-1",
        "recording_file": recording_file,
        "duration_seconds": total_duration,
        "provider": "faster-whisper",
        "model": args.model,
        "start_offset_seconds": args.start_offset_seconds,
        "segments": raw_segments,
        "metadata": {
            "segment_count": len(raw_segments),
            "chunk_audio_path": args.audio,
        },
    }

    os.makedirs(os.path.dirname(os.path.abspath(args.output)), exist_ok=True)
    with open(args.output, "w") as f:
        json.dump(raw_transcript, f, indent=2)

    progress(100, "Raw transcript written")

    print(json.dumps({
        "type": "complete",
        "segments": len(raw_segments),
        "duration_seconds": int(total_duration),
    }), flush=True)


if __name__ == "__main__":
    main()
