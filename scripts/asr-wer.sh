#!/usr/bin/env bash
# ASR WER 测量(meta 77_ 计划步 5)。
#
# 用法:
#   ./scripts/asr-wer.sh --audio-dir path/to/wavs
#   ./scripts/asr-wer.sh --audio-dir path/to/wavs --only wer-01,wer-02
#
# 音频要求: <id>.wav, 16 kHz 单声道 PCM16。id 见
# internal/voicepoc/testdata/asr_wer_samples.json
#
# 录音建议(见 docs/52): 用手机自带录音即可, 每条之间停顿一秒, 不要刻意放慢
# 或"念标准" —— 要测的就是自然状态下的口音。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -f "$ROOT/.env.volc.local" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$ROOT/.env.volc.local"
  set +a
fi
if [[ -z "${VOLC_POC_API_KEY:-}" && -n "${VOLC_SPEECH_API_KEY:-}" ]]; then
  VOLC_POC_API_KEY="${VOLC_SPEECH_API_KEY}"
fi
if [[ -z "${VOLC_SPEECH_API_KEY:-}${VOLC_POC_API_KEY:-}" ]]; then
  echo "VOLC_SPEECH_API_KEY / VOLC_POC_API_KEY is empty." >&2
  echo "Create $ROOT/.env.volc.local from configs/volc.env.example." >&2
  exit 1
fi

exec go run ./cmd/asr-wer "$@"
