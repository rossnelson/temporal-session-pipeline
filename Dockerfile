# Stage 1: Build Go binary
FROM golang:1.25-alpine AS go-build
RUN apk --no-cache add build-base git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -o /worker ./cmd/worker

# Stage 2: Runtime
FROM node:20-bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 python3-pip python3-venv \
    ffmpeg git \
    && rm -rf /var/lib/apt/lists/*

# Install Claude Code CLI
RUN npm install -g @anthropic-ai/claude-code

# Install GitHub CLI
RUN apt-get update && apt-get install -y --no-install-recommends curl && \
    curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | tee /etc/apt/sources.list.d/github-cli.list > /dev/null && \
    apt-get update && apt-get install -y gh && \
    rm -rf /var/lib/apt/lists/*

# Install Python deps
COPY scripts/requirements.txt /app/scripts/requirements.txt
RUN python3 -m venv /app/venv && \
    /app/venv/bin/pip install --no-cache-dir -r /app/scripts/requirements.txt

COPY --from=go-build /worker /app/worker
COPY scripts/ /app/scripts/

ENV PATH="/app:/app/venv/bin:${PATH}"
WORKDIR /app
CMD ["/app/worker"]
