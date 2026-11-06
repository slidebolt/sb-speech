# Git Workflow for sb-speech

This repository contains the Slidebolt Speech service, which turns audio into text one utterance at a time. It produces a standalone binary.

## Dependencies
- **Internal:**
  - `sb-contract`, `sb-messenger-sdk`, `sb-runtime`: Standard service dependencies.
  - `sb-speech-sdk`: The contract other binaries use.
- **External:**
  - `whisper.cpp` and a model file at runtime.

## Build Process
- **Type:** Go Application (Service).
- **Command:** `go build -o sb-speech ./cmd/sb-speech`
- **Validation:** `go test ./...`. Real speech recognition is covered by the `voice` suite in `sb-e2e`.
