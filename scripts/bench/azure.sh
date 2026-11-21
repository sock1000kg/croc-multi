#!/usr/bin/env bash
# M3 benchmark (docs/phan-cong.md): time croc 1-1 against multi-send through
# the same relay, from one machine. Each case runs twice.
#
#   CROC_PASS=<relay password> scripts/bench/azure.sh <croc binary> <relay ip:9009> [size MB] [N...]
#
# 1-1 is forced through the relay (--no-local --transport relay), otherwise it
# would take a LAN shortcut because sender and receiver share a machine.
# Receivers for N > 1 share this machine's downlink, so only N=1 compares
# cleanly with 1-1; use real separate machines for larger N.
set -euo pipefail
B=$(realpath "$1"); R=$2; MB=${3:-300}; shift $(( $# < 3 ? $# : 3 )); NS=("${@:-1}")
[ -n "${CROC_PASS:-}" ] || { echo "set CROC_PASS" >&2; exit 2; }
W=$(mktemp -d); trap 'rm -rf "$W"' EXIT; cd "$W"
head -c "${MB}M" /dev/urandom > f.bin; SRC=$(sha256sum f.bin | cut -c1-12)
t() { date +%s.%N; }
res() { python3 -c "d=$2-$1; print(f'{d:7.2f}s {$MB*8.388608/d:7.1f} Mbit/s per receiver')"; }
for run in 1 2; do
  rm -rf o*; mkdir o11; C=bench-one-$run-$RANDOM-$RANDOM
  CROC_SECRET=$C timeout 900 "$B" --relay "$R" send --no-local --transport relay f.bin >/dev/null 2>&1 & SP=$!
  sleep 2; T0=$(t)
  (cd o11 && CROC_SECRET=$C timeout 900 "$B" --yes --relay "$R" >/dev/null 2>&1); T1=$(t); wait $SP
  echo "1-1 run $run:      $(res $T0 $T1) ok=$([ "$(sha256sum o11/f.bin | cut -c1-12)" = "$SRC" ] && echo y || echo NO)"
  for N in "${NS[@]}"; do
    C=bench-multi-$N-$run-$RANDOM
    timeout 900 "$B" --relay "$R" send --receivers "$N" --timeout 0 --code "$C" f.bin > sm.log 2>&1 & SP=$!
    sleep 2; T0=$(t); P=()
    for i in $(seq "$N"); do mkdir -p "o$N-$i"; (cd "o$N-$i" && timeout 900 "$B" --relay "$R" --multi "$C" >/dev/null 2>&1) & P+=($!); done
    for p in "${P[@]}"; do wait "$p"; done; T1=$(t); wait $SP
    ok=$(sha256sum o"$N"-*/f.bin | cut -c1-12 | sort -u)
    echo "multi N=$N run $run:  $(res $T0 $T1) $(grep -o 'uploaded [0-9]*' sm.log) ok=$([ "$ok" = "$SRC" ] && echo y || echo NO)"
  done
done
