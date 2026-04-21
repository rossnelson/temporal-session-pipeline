#!/usr/bin/env python3
"""Pyannote speaker diarization — reads a raw transcript JSON from transcribe_audio.py,
assigns speaker labels, and writes a final session transcript JSON.

Offset convention (see plan §2.1 and §8.4):
  - Raw transcript start_ms / end_ms are SESSION-ABSOLUTE (already shifted by
    transcribe_audio.py using --start-offset-seconds).
  - pyannote receives the CHUNK audio file and emits turns in CHUNK-LOCAL seconds
    (relative to time 0 of that file).
  - Alignment strategy: convert pyannote turns to session-absolute BEFORE computing
    overlap.  Specifically:
        turn_abs_start_s = turn.start + start_offset_seconds
        turn_abs_end_s   = turn.end   + start_offset_seconds
    Then compare against seg_start_s = start_ms / 1000.0 and seg_end_s = end_ms / 1000.0.
  - final start_ms / end_ms in the output are COPIED from the raw transcript — already
    session-absolute, no re-shifting needed.
"""
import argparse
import json
import os
import time


def progress(pct, msg):
    """Print a JSON progress line to stdout."""
    print(json.dumps({"type": "progress", "percent": pct, "message": msg}), flush=True)


def main():
    parser = argparse.ArgumentParser(
        description="Speaker diarization with pyannote — assigns speaker labels to raw transcript segments"
    )
    parser.add_argument("--audio", required=True, help="Path to the same chunk WAV passed to transcribe_audio.py")
    parser.add_argument(
        "--raw-transcript",
        required=True,
        help="Path to the raw transcript JSON emitted by transcribe_audio.py",
    )
    parser.add_argument("--output", required=True, help="Path to write final transcript JSON")
    parser.add_argument("--min-speakers", type=int, default=5)
    parser.add_argument("--max-speakers", type=int, default=8)
    parser.add_argument(
        "--label-prefix",
        default="",
        help="Prefix prepended to every speaker label (e.g., 'chunk_0:'). Default empty.",
    )
    parser.add_argument(
        "--compute-type",
        default="int8",
        help="Echoed into output metadata.quantization. Default 'int8'.",
    )
    args = parser.parse_args()

    # Step 1: Load raw transcript
    progress(5, "Loading raw transcript...")
    with open(args.raw_transcript) as f:
        raw = json.load(f)

    raw_segments = raw["segments"]           # list of {start_ms, end_ms, text} — SESSION-ABSOLUTE
    start_offset_seconds = raw.get("start_offset_seconds", 0.0)
    total_duration = raw.get("duration_seconds", 0.0)
    model_name = raw.get("model", "unknown")
    recording_file = raw.get("recording_file", os.path.basename(args.audio))

    progress(10, f"Loaded {len(raw_segments)} raw segments; offset={start_offset_seconds}s")

    # Step 2: Fail-fast on missing HF_TOKEN before any expensive model loading
    hf_token = os.environ.get("HF_TOKEN")
    if not hf_token:
        raise RuntimeError(
            "HF_TOKEN environment variable is required for speaker diarization. "
            "Silent fallback removed — set the token or explicitly skip diarization."
        )

    # Step 3: Load pyannote pipeline
    progress(10, "Loading pyannote diarization pipeline...")
    from pyannote.audio import Pipeline as DiarizationPipeline

    diarization_pipeline = DiarizationPipeline.from_pretrained(
        "pyannote/speaker-diarization-3.1",
        use_auth_token=hf_token,
    )
    progress(40, "Pipeline loaded")

    # Step 4: Run diarization on the chunk audio
    # pyannote sees the chunk file → emits chunk-local timestamps (start=0 at chunk start).
    progress(40, "Running diarization...")
    diarization = diarization_pipeline(
        args.audio,
        min_speakers=args.min_speakers,
        max_speakers=args.max_speakers,
    )
    progress(70, "Diarization complete")

    # Step 5: Build speaker turns in SESSION-ABSOLUTE seconds.
    # pyannote turn.start / turn.end are chunk-local; add start_offset_seconds to align
    # with the session-absolute ms values stored in the raw transcript.
    # See plan §2.1 and §8.4 for rationale.
    speaker_turns = []
    for turn, _, speaker in diarization.itertracks(yield_label=True):
        speaker_turns.append((
            turn.start + start_offset_seconds,   # session-absolute start (seconds)
            turn.end + start_offset_seconds,     # session-absolute end   (seconds)
            speaker,
        ))

    # Step 6: Assign speakers to raw transcript segments via max-overlap
    progress(70, "Mapping speakers to segments...")
    transcript_segments = []
    for seg in raw_segments:
        seg_start_s = seg["start_ms"] / 1000.0
        seg_end_s = seg["end_ms"] / 1000.0
        seg_duration = max(0.001, seg_end_s - seg_start_s)

        raw_speaker_label = "UNKNOWN"
        best_overlap = 0.0
        for turn_start_s, turn_end_s, speaker in speaker_turns:
            overlap_start = max(seg_start_s, turn_start_s)
            overlap_end = min(seg_end_s, turn_end_s)
            overlap = max(0.0, overlap_end - overlap_start)
            if overlap > best_overlap:
                best_overlap = overlap
                raw_speaker_label = speaker

        prefixed_label = f"{args.label_prefix}{raw_speaker_label}" if args.label_prefix else raw_speaker_label

        transcript_segments.append({
            "speaker": prefixed_label,
            "start_ms": seg["start_ms"],   # copied from raw — already session-absolute
            "end_ms": seg["end_ms"],
            "text": seg["text"],
            "confidence": min(1.0, best_overlap / seg_duration),
        })

    progress(80, "Speaker assignment complete")

    # Step 7: Fail-fast sanity check — diarization must produce ≥ 2 distinct raw speakers
    # Strip any label-prefix before counting so we count pyannote labels, not prefixed ones.
    if args.label_prefix:
        raw_labels = set(
            s["speaker"][len(args.label_prefix):] for s in transcript_segments
        )
    else:
        raw_labels = set(s["speaker"] for s in transcript_segments)

    if len(raw_labels) < 2:
        raise RuntimeError(
            f"Diarization produced only {len(raw_labels)} distinct speaker(s) "
            f"for {len(transcript_segments)} segments. Expected multiple speakers in a D&D session. "
            f"This usually indicates a pyannote configuration or model download failure."
        )

    # Step 8: Build output JSON
    progress(90, "Building transcript JSON...")

    speaker_durations = {}
    for seg in transcript_segments:
        spk = seg["speaker"]
        dur = (seg["end_ms"] - seg["start_ms"]) / 1000.0
        speaker_durations[spk] = speaker_durations.get(spk, 0) + dur

    unique_speakers = sorted(set(s["speaker"] for s in transcript_segments))
    word_count = sum(len(s["text"].split()) for s in transcript_segments)

    # Derive session_id from output basename (strip .json)
    session_id = os.path.basename(args.output).replace(".json", "")

    transcript = {
        "schema_version": "final-1",
        "session_id": session_id,
        "recording_file": recording_file,
        "duration_seconds": total_duration,
        "provider": "faster-whisper",
        "model": model_name,
        "quantization": args.compute_type,
        "cost_usd": 0.0,
        "speakers": unique_speakers,
        "segments": transcript_segments,
        "metadata": {
            "word_count": word_count,
            "processing_time_seconds": 0,
            "speaker_durations": speaker_durations,
        },
    }

    os.makedirs(os.path.dirname(os.path.abspath(args.output)), exist_ok=True)
    with open(args.output, "w") as f:
        json.dump(transcript, f, indent=2)

    progress(100, "Final transcript written")

    print(json.dumps({
        "type": "complete",
        "segments": len(transcript_segments),
        "speakers": len(unique_speakers),
        "duration_seconds": int(total_duration),
    }), flush=True)


if __name__ == "__main__":
    main()
