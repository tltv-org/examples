#!/bin/bash
# Generate test HLS content for TLTV examples
# Usage: ./generate-stream.sh [output-dir]
# Requires: ffmpeg

set -e
DIR="${1:-media}"
mkdir -p "$DIR"

echo "Generating 30s test stream -> $DIR/"
ffmpeg -y -loglevel warning \
  -f lavfi -i "testsrc2=size=640x360:rate=30:duration=30" \
  -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=30" \
  -c:v libx264 -preset ultrafast -profile:v baseline -level 3.0 \
  -c:a aac -b:a 96k \
  -f hls -hls_time 2 -hls_list_size 0 \
  -hls_segment_filename "$DIR/seg-%03d.ts" \
  "$DIR/stream.m3u8"

echo "Done. $(ls "$DIR"/*.ts 2>/dev/null | wc -l) segments in $DIR/"
