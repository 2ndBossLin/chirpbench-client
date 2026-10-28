#!/usr/bin/env bash
# 构建 ChirpBench 本地客户端（单文件、零依赖）
# 用法：./build.sh            仅构建当前平台
#       ./build.sh all        Linux + Windows 一起构建
set -euo pipefail
cd "$(dirname "$0")"

mkdir -p web out
cp ../index.html web/index.html
for f in chirpbench-icon.png favicon-32.png favicon-180.png favicon-512.png logo.jpg chart.umd.min.js html2canvas.min.js; do
  [ -f "../$f" ] && cp "../$f" "web/$f"
done

go mod init chirpbench-client 2>/dev/null || true
go mod tidy

build() {
  local goos=$1 goarch=$2 out=$3
  echo "构建 $goos/$goarch -> out/$out"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath -buildvcs=false -ldflags "-s -w" -o "out/$out" .
}

case "${1:-local}" in
  all)
    build linux  amd64 chirpbench-client-linux-amd64
    build windows amd64 chirpbench-client-windows-amd64.exe
    ;;
  windows) build windows amd64 chirpbench-client-windows-amd64.exe ;;
  linux)   build linux   amd64 chirpbench-client-linux-amd64 ;;
  *)
    go build -trimpath -buildvcs=false -ldflags "-s -w" -o out/chirpbench-client .
    ;;
esac

ls -lh out/
