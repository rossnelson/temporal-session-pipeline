# Temporal Session Pipeline

A reference implementation of a long-running audio-processing pipeline built on
[Temporal](https://temporal.io). It accompanies the blog post in
[`docs/blog-post.md`](docs/blog-post.md), which walks through the Temporal
features this repo exercises: long `StartToCloseTimeout` + `HeartbeatTimeout`,
`workflow.SideEffect`, signals, parallel activities via `workflow.Go` +
`workflow.Await`, and heartbeats driven by child-process stdout. Read the blog
post for the narrative; read the code for the shape.

## Architecture

```
recording.wav
  │
  ├─▶ extract date from metadata  (or wait on SetSessionDate signal)
  ├─▶ concat + silence-align chunk (~20 min chunks)
  ├─▶ per chunk: Whisper transcribe → pyannote diarize
  ├─▶ merge chunk transcripts      (session-absolute timestamps)
  ├─▶ propose speaker mappings     (or wait on ConfirmSpeakers signal)
  ├─▶ apply speaker mappings
  ├─▶ parallel: generate recap  +  detect highlights
  ├─▶ extract audio clips for each highlight
  ├─▶ assemble highlight reel
  ├─▶ generate highlight content pages
  └─▶ commit, push, open pull request
```

## Binaries

- `dio` — CLI for starting workflows and sending signals
  (`process`, `confirm-speakers`, `set-date`).
- `worker` — Temporal worker that hosts the workflow and all activities.

## Running locally

1. Start a local Temporal dev server:
   [Temporal CLI](https://docs.temporal.io/cli) — `temporal server start-dev`.
2. Build the binaries:
   ```
   go build -o dio ./cmd/dio
   go build -o worker ./cmd/worker
   ```
3. In one shell: `./worker`
4. In another shell: `./dio process /path/to/recording.wav --date 2026-04-17`

## Python dependencies

Whisper and pyannote are invoked via `scripts/transcribe_audio.py` and
`scripts/diarize_audio.py`. Install their deps:

```
pip install -r scripts/requirements.txt
```

Pyannote requires a Hugging Face token (accept the pyannote model licenses
first). Export it as `HF_TOKEN`.

## Environment variables

| Var | Purpose |
|---|---|
| `TEMPORAL_HOST` | Temporal frontend address (default `localhost:7233`) |
| `PIPELINE_DATA_DIR` | Root directory for session artifacts (default `/data`) |
| `PIPELINE_GH_OWNER` | GitHub owner of the repo that will receive the PR |
| `PIPELINE_GH_REPO` | GitHub repo name that will receive the PR |
| `GITHUB_TOKEN` or `GH_TOKEN` | Auth for the `gh` CLI used by the commit/PR activities |
| `CLAUDE_CODE_OAUTH_TOKEN` | Claude Code CLI authentication |
| `HF_TOKEN` | Hugging Face token for pyannote model download |
| `PYTHON_PATH` | Python interpreter path (default `python3`) |

## Docker

The included `Dockerfile` builds the `worker` binary and bundles Python,
ffmpeg, the GitHub CLI, and the Claude Code CLI:

```
docker build -t session-worker .
docker run --rm -e TEMPORAL_HOST=... session-worker
```

## Initial commit author

The initial commit in this repo is authored as
`Session Pipeline Bot <pipeline-bot@example.com>`. This is the same identity
the pipeline itself uses when committing generated content (see
`internal/pipeline/activity_prepare_workspace.go`). It was chosen so that the
importer's personal git identity doesn't leak into a reference repo. Future
contributors should use their own git identity for subsequent commits.

## `.tool-versions`

This repo pins `golang 1.25` for users of [`asdf`](https://asdf-vm.com/) or
[`mise`](https://mise.jdx.dev/). The upstream source repo also pinned Python
and Hugo versions; those are not relevant here — Python deps are installed via
`pip`/the Dockerfile, and this repo has no Hugo site.

## What this repo is not

- Not production-ready. It is a reference for Temporal patterns; error-handling
  and observability are deliberately minimal.
- Not a Hugo site generator. Some activity prompts reference `content/` paths
  because the original pipeline commits into a Hugo site — that site is not
  included here.
- Not a Slack bot. The original pipeline could receive the `ConfirmSpeakers`
  signal from a Slack thread; this reference keeps only the CLI path.

## See also

- Blog post: [`docs/blog-post.md`](docs/blog-post.md)
- Temporal documentation: <https://docs.temporal.io>
