# AURA SQL CLI Tools

## Overview

This directory contains the command-line tools for AURA SQL:

- **`cmd/smoke/`** - Sanity check (Phase 0) - verifies the core storage contract works
- **`cmd/aura/`** - Main REPL (Phase 3) - Interactive SQL shell for end users
- **`cmd/demo/`** - Performance demo (Phase 3) - Shows index speed-up

## Building

```bash
# Build all CLI tools
make build

# Or build individually
go build -o bin/aura ./cmd/aura
go build -o bin/demo ./cmd/demo
go build -o bin/smoke ./cmd/smoke