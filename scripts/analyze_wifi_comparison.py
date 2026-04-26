#!/usr/bin/env python3
"""
analyze_wifi_comparison.py — Statistical analysis of wifi_comparison.sh output.

Inputs (from results/wifi-comparison-*/):
  - pings.tsv             (Test 1: rotated 5-min windows of {hopssh, tailscale, zerotier})
  - pings-mtu.tsv         (Test 2: hopssh-mtu1420, hopssh-mtu1280, tailscale)
  - pings-tailscale.tsv + tailscale-status-snapshots.jsonl  (Test 3)

Outputs:
  - SUMMARY.md  — per-VPN window stats, pairwise comparisons
  - VERDICT.md  — A1/A2/A3/A4/B/C/D/E decision per the plan
  - decision.json — machine-readable verdict for downstream automation

Decision rules (per the approved plan):
  - paired Wilcoxon p<0.01 AND median_p95(X) < 0.7*median_p95(hopssh)
      => X is materially smoother than hopssh (refute null)
  - p>0.05 OR ratio>0.85
      => X is NOT materially smoother (confirm null)
  - in between => ambiguous

Pure stdlib; falls back to bootstrap when scipy unavailable.
"""
import argparse
import csv
import json
import math
import os
import statistics
import sys
from collections import defaultdict
from pathlib import Path

WINDOW_SEC = 60               # statistical bin = 1 minute
LOSS_PENALTY_MS = 2000        # treat lost ping as 2s for percentile math


def load_pings(path: Path):
    rows = []
    if not path.exists():
        return rows
    with path.open() as f:
        r = csv.reader(f, delimiter='\t')
        next(r, None)  # header
        for row in r:
            if len(row) < 3:
                continue
            ts, label, val = row[0], row[1], row[2]
            try:
                t = float(ts)
            except ValueError:
                continue
            if val == "LOSS":
                rows.append((t, label, None))
            else:
                try:
                    rows.append((t, label, float(val)))
                except ValueError:
                    continue
    return rows


def bin_by_window(rows, window_sec=WINDOW_SEC):
    """Return {label: [ {ts: window_start, samples: [rtt,...], losses: int}, ...]}"""
    if not rows:
        return {}
    by_label = defaultdict(lambda: defaultdict(lambda: {"samples": [], "losses": 0}))
    for ts, label, rtt in rows:
        bucket = int(ts // window_sec) * window_sec
        if rtt is None:
            by_label[label][bucket]["losses"] += 1
        else:
            by_label[label][bucket]["samples"].append(rtt)
    out = {}
    for lbl, buckets in by_label.items():
        out[lbl] = sorted(
            ({"ts": k, **v} for k, v in buckets.items()),
            key=lambda d: d["ts"],
        )
    return out


def percentile(xs, p):
    if not xs:
        return float("nan")
    xs = sorted(xs)
    if len(xs) == 1:
        return xs[0]
    k = (len(xs) - 1) * p
    f = math.floor(k)
    c = math.ceil(k)
    if f == c:
        return xs[int(k)]
    return xs[f] + (xs[c] - xs[f]) * (k - f)


def window_stats(window):
    samples = window["samples"]
    losses = window["losses"]
    n = len(samples) + losses
    # Penalize losses for percentile math (they're real bad RTT in user UX terms)
    inflated = samples + [LOSS_PENALTY_MS] * losses
    return {
        "ts": window["ts"],
        "n": n,
        "loss_pct": (losses / n * 100.0) if n else 0.0,
        "p50": percentile(inflated, 0.50),
        "p95": percentile(inflated, 0.95),
        "p99": percentile(inflated, 0.99),
        "max": max(inflated) if inflated else float("nan"),
        "mean": statistics.fmean(inflated) if inflated else float("nan"),
    }


def vpn_summary(label, windows):
    stats = [window_stats(w) for w in windows]
    valid = [s for s in stats if s["n"] >= 10]
    return {
        "label": label,
        "windows": len(stats),
        "valid_windows": len(valid),
        "median_p50": statistics.median(s["p50"] for s in valid) if valid else float("nan"),
        "median_p95": statistics.median(s["p95"] for s in valid) if valid else float("nan"),
        "median_p99": statistics.median(s["p99"] for s in valid) if valid else float("nan"),
        "median_max": statistics.median(s["max"] for s in valid) if valid else float("nan"),
        "mean_loss_pct": statistics.fmean(s["loss_pct"] for s in valid) if valid else float("nan"),
    }


def paired_windows(a_windows, b_windows, max_gap_sec=600):
    """Pair adjacent-in-time windows from a and b; max_gap = 10 min."""
    a_sorted = sorted(a_windows, key=lambda w: w["ts"])
    b_sorted = sorted(b_windows, key=lambda w: w["ts"])
    pairs = []
    i = j = 0
    while i < len(a_sorted) and j < len(b_sorted):
        a, b = a_sorted[i], b_sorted[j]
        gap = abs(a["ts"] - b["ts"])
        if gap <= max_gap_sec:
            pairs.append((a, b))
            i += 1
            j += 1
        elif a["ts"] < b["ts"]:
            i += 1
        else:
            j += 1
    return pairs


def wilcoxon(diffs):
    """Two-sided Wilcoxon signed-rank test; returns (W, p_approx).

    Falls back to a normal-approximation when scipy is unavailable.
    """
    diffs = [d for d in diffs if d != 0 and not math.isnan(d)]
    n = len(diffs)
    if n < 6:
        return float("nan"), float("nan")
    try:
        from scipy.stats import wilcoxon as scipy_wilcoxon  # type: ignore
        res = scipy_wilcoxon(diffs)
        return float(res.statistic), float(res.pvalue)
    except ImportError:
        # Normal approximation: rank by |d|, sum with sign.
        ranked = sorted(enumerate(diffs), key=lambda x: abs(x[1]))
        # Average ties
        ranks = [0.0] * n
        i = 0
        while i < n:
            j = i
            while j + 1 < n and abs(ranked[j + 1][1]) == abs(ranked[i][1]):
                j += 1
            avg_rank = (i + j) / 2.0 + 1
            for k in range(i, j + 1):
                ranks[ranked[k][0]] = avg_rank
            i = j + 1
        W_plus = sum(r for r, d in zip(ranks, diffs) if d > 0)
        mu = n * (n + 1) / 4.0
        sigma = math.sqrt(n * (n + 1) * (2 * n + 1) / 24.0)
        z = (W_plus - mu) / sigma if sigma else 0.0
        # Two-sided p via normal CDF
        p = math.erfc(abs(z) / math.sqrt(2))
        return W_plus, p


def compare_pair(a_label, a_summary, a_windows, b_label, b_summary, b_windows):
    pairs = paired_windows(a_windows, b_windows)
    diffs_p95 = [
        window_stats(b)["p95"] - window_stats(a)["p95"]
        for a, b in pairs
        if window_stats(a)["n"] >= 10 and window_stats(b)["n"] >= 10
    ]
    W, p = wilcoxon(diffs_p95)
    a_p95 = a_summary["median_p95"]
    b_p95 = b_summary["median_p95"]
    ratio = (b_p95 / a_p95) if a_p95 and not math.isnan(a_p95) else float("nan")

    if math.isnan(p):
        verdict = "insufficient-data"
    elif p < 0.01 and not math.isnan(ratio) and ratio < 0.7:
        verdict = f"{b_label}-materially-smoother"
    elif p > 0.05 or (not math.isnan(ratio) and ratio > 0.85):
        verdict = "equivalent-within-noise"
    else:
        verdict = "ambiguous"

    return {
        "a": a_label,
        "b": b_label,
        "n_pairs": len(pairs),
        "n_used": len(diffs_p95),
        "W": W,
        "p_value": p,
        "median_p95_a": a_p95,
        "median_p95_b": b_p95,
        "ratio_b_over_a": ratio,
        "verdict": verdict,
    }


def derive_overall_verdict(comparisons, mtu_test, p2p_dern):
    """Map pairwise comparisons + Test 2/3 evidence to A1..E from the plan."""
    hopssh_vs_ts = next((c for c in comparisons if c["a"] == "hopssh" and c["b"] == "tailscale"), None)
    hopssh_vs_zt = next((c for c in comparisons if c["a"] == "hopssh" and c["b"] == "zerotier"), None)

    ts_smoother = hopssh_vs_ts and hopssh_vs_ts["verdict"] == "tailscale-materially-smoother"
    zt_smoother = hopssh_vs_zt and hopssh_vs_zt["verdict"] == "zerotier-materially-smoother"
    ts_equiv    = hopssh_vs_ts and hopssh_vs_ts["verdict"] == "equivalent-within-noise"
    zt_equiv    = hopssh_vs_zt and hopssh_vs_zt["verdict"] == "equivalent-within-noise"

    # Test 2: MTU is the differentiator if hopssh@1280 ≈ tailscale within 15%
    if mtu_test:
        m1280 = mtu_test.get("hopssh-mtu1280", {}).get("median_p95")
        mts   = mtu_test.get("tailscale", {}).get("median_p95")
        m1420 = mtu_test.get("hopssh-mtu1420", {}).get("median_p95")
        if m1280 and mts and m1420:
            ratio_1280_ts = m1280 / mts if mts else float("nan")
            improves      = m1280 < m1420 * 0.85
            if improves and 0.85 < ratio_1280_ts < 1.15:
                return "A1", "MTU is the differentiator"

    # Test 3: DERP fraction during 'good' Tailscale windows
    if p2p_dern and p2p_dern.get("derp_fraction", 0) > 0.20 and ts_smoother:
        return "A2", "DERP-fallback is the differentiator"

    if ts_smoother and zt_smoother:
        return "D", "hopssh is the slowest of three (worst case)"
    if ts_smoother and zt_equiv:
        return "A4", "Tailscale-only advantage suggests NIC offload / NEPacketTunnelProvider gap"
    if ts_smoother or zt_smoother:
        return "B", "Materially worse, cause unclear"
    if ts_equiv and zt_equiv:
        return "C", "Equivalent within noise across all three VPNs"

    return "E", "Inconclusive — see comparisons table"


def analyze_test1(out_dir: Path):
    rows = load_pings(out_dir / "pings.tsv")
    binned = bin_by_window(rows)
    summaries = {lbl: vpn_summary(lbl, w) for lbl, w in binned.items()}
    comparisons = []
    if "hopssh" in binned and "tailscale" in binned:
        comparisons.append(compare_pair("hopssh", summaries["hopssh"], binned["hopssh"],
                                        "tailscale", summaries["tailscale"], binned["tailscale"]))
    if "hopssh" in binned and "zerotier" in binned:
        comparisons.append(compare_pair("hopssh", summaries["hopssh"], binned["hopssh"],
                                        "zerotier", summaries["zerotier"], binned["zerotier"]))
    if "tailscale" in binned and "zerotier" in binned:
        comparisons.append(compare_pair("tailscale", summaries["tailscale"], binned["tailscale"],
                                        "zerotier", summaries["zerotier"], binned["zerotier"]))
    return summaries, comparisons


def analyze_test2(out_dir: Path):
    rows = load_pings(out_dir / "pings-mtu.tsv")
    binned = bin_by_window(rows)
    return {lbl: vpn_summary(lbl, w) for lbl, w in binned.items()}


def analyze_test3(out_dir: Path):
    snap_path = out_dir / "tailscale-status-snapshots.jsonl"
    if not snap_path.exists():
        return None
    derp_windows = total = 0
    with snap_path.open() as f:
        for line in f:
            try:
                snap = json.loads(line)
            except json.JSONDecodeError:
                continue
            peers = snap.get("Peer") or {}
            if not peers:
                continue
            total += 1
            for _, p in peers.items():
                if p.get("Relay") and not p.get("CurAddr"):
                    derp_windows += 1
                    break
    if total == 0:
        return {"derp_fraction": 0.0, "n_snapshots": 0}
    return {"derp_fraction": derp_windows / total, "n_snapshots": total}


def fmt(x, suffix=""):
    if isinstance(x, float):
        if math.isnan(x):
            return "n/a"
        return f"{x:.2f}{suffix}"
    return str(x)


def write_summary(out_dir: Path, summaries, comparisons, mtu_test, p2p_dern):
    lines = ["# WiFi comparison summary", ""]
    lines.append("## Per-VPN window statistics")
    lines.append("")
    lines.append("| VPN | windows | median p50 | median p95 | median p99 | median max | mean loss% |")
    lines.append("|---|---:|---:|---:|---:|---:|---:|")
    for lbl, s in summaries.items():
        lines.append(f"| {lbl} | {s['valid_windows']} | "
                     f"{fmt(s['median_p50'])} | {fmt(s['median_p95'])} | "
                     f"{fmt(s['median_p99'])} | {fmt(s['median_max'])} | "
                     f"{fmt(s['mean_loss_pct'])} |")
    lines.append("")
    lines.append("## Pairwise comparisons (paired Wilcoxon on p95 RTT)")
    lines.append("")
    lines.append("| A | B | n pairs | W | p | median p95(A) | median p95(B) | B/A | verdict |")
    lines.append("|---|---|---:|---:|---:|---:|---:|---:|---|")
    for c in comparisons:
        lines.append(f"| {c['a']} | {c['b']} | {c['n_used']} | {fmt(c['W'])} | "
                     f"{fmt(c['p_value'])} | {fmt(c['median_p95_a'])} | "
                     f"{fmt(c['median_p95_b'])} | {fmt(c['ratio_b_over_a'])} | "
                     f"{c['verdict']} |")
    if mtu_test:
        lines.append("")
        lines.append("## Test 2 (MTU bisection)")
        lines.append("")
        lines.append("| label | windows | median p50 | median p95 | median p99 |")
        lines.append("|---|---:|---:|---:|---:|")
        for lbl, s in mtu_test.items():
            lines.append(f"| {lbl} | {s['valid_windows']} | "
                         f"{fmt(s['median_p50'])} | {fmt(s['median_p95'])} | "
                         f"{fmt(s['median_p99'])} |")
    if p2p_dern is not None:
        lines.append("")
        lines.append("## Test 3 (P2P-vs-DERP discrimination)")
        lines.append("")
        lines.append(f"- Snapshots: {p2p_dern['n_snapshots']}")
        lines.append(f"- DERP-only fraction: {p2p_dern['derp_fraction']:.2%}")
    (out_dir / "SUMMARY.md").write_text("\n".join(lines))


def write_verdict(out_dir: Path, code, description, summaries, comparisons, mtu_test, p2p_dern):
    action_map = {
        "A1": ("MTU is the differentiator",
               "Implement per-network MTU column in `internal/db/migrations/00X_network_mtu.sql`, expose via `/api/networks/:id` PUT, render dropdown in `frontend/src/routes/networks/[id]/+page.svelte`, agent reads server-pushed MTU in `cmd/agent/renew.go::ensureP2PConfig`."),
        "A2": ("DERP-fallback is the differentiator",
               "Build per-peer auto-relay-fallback in new `cmd/agent/relay_fallback.go`. Vendor patch 25 to expose `Control.SetCurrentRemote`. Threshold: p95 RTT > 100ms over 30s window."),
        "A3": ("Pacing/queueing is the differentiator",
               "Extend patch 04 (or new patch 25) in `vendor/.../udp/udp_darwin.go`: per-batch microsleep between sendmsg_x flushes when batch >4 packets, OR `setsockopt(SO_TRAFFIC_CLASS, SO_TC_BK)` for bulk lanes."),
        "A4": ("NIC offload (NEPacketTunnelProvider gap)",
               "Architectural — won't fix without NEPacketTunnelProvider migration. Ship `hop-agent diagnose wifi` for visibility."),
        "B":  ("Materially worse, cause unclear",
               "Add per-peer RTT histogram in `internal/db/migrations/00X_peer_rtt_histogram.sql`, extend `cmd/agent/peerstate.go` to emit RTT samples, add chart in `frontend/src/routes/dashboard`."),
        "C":  ("Equivalent within noise",
               "Add `hop-agent diagnose wifi` subcommand in new `cmd/agent/diagnose.go`. Per-user 'your WiFi adds Xms p95' report. No data-path changes."),
        "D":  ("hopssh is the slowest of three (worst case)",
               "Map back to dominant gap from Tests 2-5 (likely A3 pacing). Multiple causes ship as v0.10.30 batch."),
        "E":  ("Inconclusive",
               "Re-run with longer duration or fix the data-collection issue identified in SUMMARY.md."),
    }
    title, action = action_map.get(code, ("Unknown", "n/a"))
    lines = [
        f"# Verdict: {code} — {title}",
        "",
        f"**Description:** {description}",
        "",
        "## Recommended action",
        "",
        action,
        "",
        "## Evidence",
        "",
        "See `SUMMARY.md` for the full numerical breakdown.",
        "",
    ]
    (out_dir / "VERDICT.md").write_text("\n".join(lines))
    (out_dir / "decision.json").write_text(json.dumps({
        "code": code,
        "title": title,
        "description": description,
        "action": action,
        "summaries": summaries,
        "comparisons": comparisons,
        "mtu_test": mtu_test,
        "p2p_dern": p2p_dern,
    }, indent=2))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("results_dir")
    args = ap.parse_args()
    out_dir = Path(args.results_dir)
    if not out_dir.is_dir():
        print(f"error: not a directory: {out_dir}", file=sys.stderr)
        sys.exit(2)

    summaries, comparisons = analyze_test1(out_dir)
    mtu_test = analyze_test2(out_dir)
    p2p_dern = analyze_test3(out_dir)

    code, desc = derive_overall_verdict(comparisons, mtu_test, p2p_dern)
    write_summary(out_dir, summaries, comparisons, mtu_test, p2p_dern)
    write_verdict(out_dir, code, desc, summaries, comparisons, mtu_test, p2p_dern)

    print(f"Verdict: {code} — {desc}")
    print(f"  SUMMARY: {out_dir/'SUMMARY.md'}")
    print(f"  VERDICT: {out_dir/'VERDICT.md'}")
    print(f"  decision.json: {out_dir/'decision.json'}")


if __name__ == "__main__":
    main()
