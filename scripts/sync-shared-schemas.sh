#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
infra_root="${INFRA_ROOT:-$(cd "$repo_root/../fluentwork-infra" && pwd)}"

src_transport_v1="$infra_root/schemas/transport/wss-control-frames-v1.json"
src_transport_v2="$infra_root/schemas/transport/wss-control-frames-v2.json"
src_events="$infra_root/schemas/events/speech-observability-events-v1.json"
dst_root="$repo_root/schemas"

test -f "$src_transport_v1"
test -f "$src_transport_v2"
test -f "$src_events"

mkdir -p "$dst_root/transport" "$dst_root/events"
cp "$src_transport_v1" "$dst_root/transport/wss-control-frames-v1.json"
cp "$src_transport_v2" "$dst_root/transport/wss-control-frames-v2.json"
cp "$src_events" "$dst_root/events/speech-observability-events-v1.json"

echo "Synced shared schema mirrors into $dst_root"
