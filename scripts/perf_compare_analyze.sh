#!/usr/bin/env bash
# perf_compare_analyze.sh — extracts metrics from perf_compare.sh output.
#
# Two workloads per measurement:
#   - UDP iperf3 at constant 5 Mb/s for 60s — mimics RFB/H.264 screen-share.
#     Loss%, jitter, packet drop bursts isolate path quality without
#     saturating the carrier.
#   - TCP iperf3 single-stream for 30s — captures slow-start ramp curve
#     (the "first 30 sec choppy" symptom shape).
#
# Plus: ping-under-load for RTT-over-time, hop-agent log for Nebula events.

set -uo pipefail

OUTDIR="${1:-}"
if [ -z "$OUTDIR" ] || [ ! -d "$OUTDIR" ]; then
  echo "Usage: $0 <results-dir>" >&2
  exit 1
fi

SUMMARY="$OUTDIR/summary.txt"
> "$SUMMARY"

emit() { echo "$@" | tee -a "$SUMMARY"; }

emit "==================================================================="
emit "perf_compare summary — $(basename "$OUTDIR")"
emit "==================================================================="
emit ""

# -------- helpers ---------------------------------------------------------

# Extract per-1s UDP stats: lost_packets / packets / jitter_ms / bits_per_second
extract_udp_intervals() {
  local json="$1"
  jq -r '
    if .intervals then
      .intervals
      | to_entries
      | map(.key as $i | .value.sum as $s
              | "\($i+1) \(($s.lost_packets // 0)) \(($s.packets // 0)) \(($s.jitter_ms // 0)) \(($s.bits_per_second // 0) / 1e6)")
      | join("\n")
    else "" end
  ' "$json" 2>/dev/null
}

# Extract per-1s TCP throughput in Mb/s
extract_tcp_intervals() {
  local json="$1"
  jq -r '
    if .intervals then
      .intervals
      | to_entries
      | map(.key as $i | .value.sum.bits_per_second as $bps
              | "\($i+1) \($bps / 1e6)")
      | join("\n")
    else "" end
  ' "$json" 2>/dev/null
}

# Extract RTTs from ping output
extract_rtt() {
  local txt="$1"
  grep -oE 'time=[0-9.]+' "$txt" 2>/dev/null | sed 's/time=//' || true
}

# -------- per-pass UDP table (constant-rate path quality) -----------------

emit "UDP CONSTANT-RATE (5 Mb/s, 60s) — measures pure path quality"
emit "(High loss % or jitter spikes during early seconds = choppy startup.)"
emit ""
emit "  Pass | Net       | Loss%  | Loss%(s1-15) | Loss%(s45-60) | Jitter ms (mean / max) | RTT mean / p95 / max"
emit "  -----|-----------|--------|--------------|---------------|------------------------|---------------------"

for json in "$OUTDIR"/pass*-iperf3-udp.json; do
  [ -f "$json" ] || continue
  base="$(basename "$json" -iperf3-udp.json)"
  pass="$(echo "$base" | sed -nE 's/^pass([0-9]+)-.+/\1/p')"
  net="$(echo "$base"  | sed -nE 's/^pass[0-9]+-(.+)/\1/p')"

  intervals="$(extract_udp_intervals "$json")"
  if [ -z "$intervals" ]; then
    emit "  ${pass}    | ${net}     | (no data — iperf3 may have failed)"
    continue
  fi

  total_loss=$(echo "$intervals" | awk '{lost+=$2; tot+=$3} END{if(tot>0) printf "%.1f%%", lost*100/tot; else printf "n/a"}')
  early_loss=$(echo "$intervals" | awk 'NR<=15 {lost+=$2; tot+=$3} END{if(tot>0) printf "%.1f%%", lost*100/tot; else printf "n/a"}')
  late_loss=$(echo "$intervals" | awk 'NR>=45 && NR<=60 {lost+=$2; tot+=$3} END{if(tot>0) printf "%.1f%%", lost*100/tot; else printf "n/a"}')
  jitter_mean=$(echo "$intervals" | awk '{s+=$4} END{if(NR>0) printf "%.1f", s/NR; else printf "n/a"}')
  jitter_max=$(echo "$intervals" | awk 'BEGIN{m=0}{if($4+0>m) m=$4+0} END{printf "%.1f", m}')

  rttfile="${json%-iperf3-udp.json}-rtt.txt"
  rtts="$(extract_rtt "$rttfile")"
  if [ -n "$rtts" ]; then
    n=$(echo "$rtts" | wc -l | awk '{print $1}')
    mean=$(echo "$rtts" | awk '{s+=$1} END{printf "%.0f", s/NR}')
    max=$(echo "$rtts"  | awk 'BEGIN{m=0}{if($1+0>m) m=$1+0} END{printf "%.0f", m}')
    p95=$(echo "$rtts"  | sort -g | awk -v n="$n" 'NR==int(n*0.95){printf "%.0f", $1}')
    rtt="${mean} / ${p95} / ${max}"
  else
    rtt="n/a"
  fi

  printf "  %-4s | %-9s | %-6s | %-12s | %-13s | %5s / %5s         | %s\n" \
    "$pass" "$net" "$total_loss" "$early_loss" "$late_loss" "$jitter_mean" "$jitter_max" "$rtt" \
    | tee -a "$SUMMARY"
done

emit ""
emit "TCP RAMP (single-stream, 30s) — captures cold-start curve shape"
emit "(Tailscale should ramp to peak in ≤5s; slow ramp on hopssh = path issue.)"
emit ""
emit "  Pass | Net       | Tput@1s | Tput@5s | Tput@10s | Tput@20s | Tput@30s | Peak"
emit "  -----|-----------|---------|---------|----------|----------|----------|------"

for json in "$OUTDIR"/pass*-iperf3-tcp.json; do
  [ -f "$json" ] || continue
  base="$(basename "$json" -iperf3-tcp.json)"
  pass="$(echo "$base" | sed -nE 's/^pass([0-9]+)-.+/\1/p')"
  net="$(echo "$base"  | sed -nE 's/^pass[0-9]+-(.+)/\1/p')"

  tput="$(extract_tcp_intervals "$json")"
  if [ -z "$tput" ]; then
    emit "  ${pass}    | ${net}     | (no data)"
    continue
  fi

  t1=$(echo "$tput"  | awk '$1==1{printf "%.1f", $2}')
  t5=$(echo "$tput"  | awk '$1==5{printf "%.1f", $2}')
  t10=$(echo "$tput" | awk '$1==10{printf "%.1f", $2}')
  t20=$(echo "$tput" | awk '$1==20{printf "%.1f", $2}')
  t30=$(echo "$tput" | awk '$1==30{printf "%.1f", $2}')
  peak=$(echo "$tput" | awk 'BEGIN{m=0}{if($2+0>m) m=$2+0} END{printf "%.1f", m}')

  printf "  %-4s | %-9s | %5sM  | %5sM  | %6sM   | %6sM   | %6sM   | %4sM\n" \
    "$pass" "$net" "${t1:-?}" "${t5:-?}" "${t10:-?}" "${t20:-?}" "${t30:-?}" "${peak:-?}" \
    | tee -a "$SUMMARY"
done

# -------- TCP ramp curve as ASCII per-second ------------------------------

emit ""
emit "TCP throughput per second (first 30s, 1 char ≈ 2 Mb/s):"
emit ""
for json in "$OUTDIR"/pass*-iperf3-tcp.json; do
  [ -f "$json" ] || continue
  base="$(basename "$json" -iperf3-tcp.json)"
  emit "  $base:"
  tput="$(extract_tcp_intervals "$json")"
  if [ -z "$tput" ]; then
    emit "    (no data)"
    continue
  fi
  echo "$tput" | awk 'NR<=30 {
    bar=""; n=int($2/2); if (n>50) n=50
    for(i=0;i<n;i++) bar=bar "#"
    printf "    s%02d  %5.1f Mb/s  %s\n", $1, $2, bar
  }' | tee -a "$SUMMARY"
  emit ""
done

# -------- UDP per-second loss timeline (where do drops happen?) ----------

emit ""
emit "UDP per-second loss% (5 Mb/s constant; spike = path issue at that moment):"
emit ""
for json in "$OUTDIR"/pass*-iperf3-udp.json; do
  [ -f "$json" ] || continue
  base="$(basename "$json" -iperf3-udp.json)"
  emit "  $base:"
  iv="$(extract_udp_intervals "$json")"
  if [ -z "$iv" ]; then
    emit "    (no data)"
    continue
  fi
  echo "$iv" | awk '$1<=30 {
    pct = ($3>0) ? ($2*100/$3) : 0
    bar=""
    n=int(pct/2); if (n>40) n=40
    for(i=0;i<n;i++) bar=bar "#"
    printf "    s%02d  loss=%5.1f%% jit=%4.1fms  %s\n", $1, pct, $4, bar
  }' | tee -a "$SUMMARY"
  emit ""
done

# -------- hop-agent path-state events -------------------------------------

emit ""
emit "hopssh path events (relayed → direct transitions, lighthouse signals):"
emit ""
for logfile in "$OUTDIR"/pass*-hopssh-agent.log; do
  [ -f "$logfile" ] || continue
  base="$(basename "$logfile")"
  emit "  $base:"
  events="$(grep -E '\(relayed\)|from="46\.10\.240\.91|Host roamed|added static host map' "$logfile" 2>/dev/null \
            | head -15 | sed -E 's/issuer=[a-f0-9]+ //; s/fingerprint=[a-f0-9]+ //; s/initiatorIndex=[0-9]+ //; s/responderIndex=[0-9]+ //; s/remoteIndex=[0-9]+ //; s/sentCachedPackets=[0-9]+ //; s/durationNs=[0-9]+ //; s/certName=[a-z0-9-]+ certVersion=1 //')"
  if [ -z "$events" ]; then
    emit "    (no relevant events)"
  else
    echo "$events" | sed 's/^/    /' | tee -a "$SUMMARY" >/dev/null
    echo "$events" | sed 's/^/    /'
  fi
  emit ""
done

emit "==================================================================="
emit "Verdict heuristics:"
emit ""
emit "  - UDP early-loss > late-loss (e.g. 8% vs 0%) → path stabilizes after"
emit "    initial seconds. Likely: relay-to-direct roam, or per-flow CGNAT"
emit "    setup. If hopssh shows this and Tailscale doesn't, that's the gap."
emit ""
emit "  - UDP jitter spike at specific second + roam log entry → path flap"
emit "    at that moment. If correlated with throughput drop, you found the"
emit "    cause."
emit ""
emit "  - TCP ramp Tput@5s ≈ Tput@30s on Tailscale, hopssh's @5s ≪ @30s →"
emit "    hopssh has slow-start headwind (likely relayed early) — fix is to"
emit "    bias direct path immediately or eliminate relay fallback delay."
emit ""
emit "  - hopssh log shows ANY '(relayed)' before the peer's direct public IP →"
emit "    Nebula initially used relay, roamed direct later. The choppy seconds"
emit "    were the relayed seconds. Fix: prevent initial relay use OR"
emit "    accelerate the direct-path discovery."
emit "==================================================================="

echo ""
echo "Full summary: $SUMMARY"
