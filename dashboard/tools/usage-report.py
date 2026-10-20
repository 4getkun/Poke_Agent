#!/usr/bin/env python3
"""Generate an HTML usage report from pokegents usage logs.

Usage:
    python3 usage-report.py                    # today's log
    python3 usage-report.py 2026-05-04         # specific date
    python3 usage-report.py 2026-05-01 2026-05-04  # date range
    python3 usage-report.py --all              # all logs

Reads from ~/.pokegents/usage/*.jsonl, writes to stdout.
"""
import json, sys, os, glob
from collections import defaultdict
from datetime import datetime, date

DATA_DIR = os.path.expanduser("~/.pokegents/usage")

def load_entries(dates):
    entries = []
    for d in dates:
        path = os.path.join(DATA_DIR, f"{d}.jsonl")
        if not os.path.exists(path):
            continue
        with open(path) as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    entries.append(json.loads(line))
                except json.JSONDecodeError:
                    continue
    return entries

def resolve_dates(args):
    if not args or args == ["--today"]:
        return [date.today().isoformat()]
    if args == ["--all"]:
        files = sorted(glob.glob(os.path.join(DATA_DIR, "*.jsonl")))
        return [os.path.basename(f).replace(".jsonl", "") for f in files]
    if len(args) == 1:
        return [args[0]]
    if len(args) == 2:
        from datetime import timedelta
        start = date.fromisoformat(args[0])
        end = date.fromisoformat(args[1])
        dates = []
        while start <= end:
            dates.append(start.isoformat())
            start += timedelta(days=1)
        return dates
    return [date.today().isoformat()]

def generate_report(entries, dates):
    # Group by agent
    by_agent = defaultdict(list)
    for e in entries:
        key = e.get("agent") or e.get("pgid", "unknown")
        by_agent[key].append(e)

    # Per-agent summaries
    agent_summaries = {}
    for agent, events in by_agent.items():
        pgid = ""
        profile = ""
        turns = [e for e in events if e["event"] == "turn_end"]
        usage = [e for e in events if e["event"] == "usage_update"]
        mcp = [e for e in events if e["event"] == "mcp_call"]
        meta = [e for e in events if e["event"] == "result_meta"]

        for e in events:
            if e.get("pgid"):
                pgid = e["pgid"]
            if e.get("profile"):
                profile = e["profile"]

        nudge_turns = [t for t in turns if t.get("prompt_kind") == "nudge"]
        user_turns = [t for t in turns if t.get("prompt_kind") == "user"]
        slash_turns = [t for t in turns if t.get("prompt_kind") == "slash"]

        total_delta = sum(t.get("tokens_delta", 0) for t in turns)
        nudge_delta = sum(t.get("tokens_delta", 0) for t in nudge_turns)
        user_delta = sum(t.get("tokens_delta", 0) for t in user_turns)

        total_duration = sum(t.get("duration_ms", 0) for t in turns)
        nudge_duration = sum(t.get("duration_ms", 0) for t in nudge_turns)

        latest_usage = max((u.get("context_used", 0) for u in usage), default=0)
        latest_window = max((u.get("context_window", 0) for u in usage), default=0)
        latest_cost = max((u.get("cost_usd", 0) for u in usage), default=0)

        mcp_counts = defaultdict(int)
        for m in mcp:
            mcp_counts[m.get("tool_name", "?")] += 1

        num_turns_api = sum(m.get("num_turns", 0) for m in meta)

        agent_summaries[agent] = {
            "pgid": pgid,
            "profile": profile,
            "total_turns": len(turns),
            "user_turns": len(user_turns),
            "nudge_turns": len(nudge_turns),
            "slash_turns": len(slash_turns),
            "error_turns": len([t for t in turns if t.get("error")]),
            "total_token_delta": total_delta,
            "nudge_token_delta": nudge_delta,
            "user_token_delta": user_delta,
            "total_duration_ms": total_duration,
            "nudge_duration_ms": nudge_duration,
            "latest_context_used": latest_usage,
            "latest_context_window": latest_window,
            "latest_cost_usd": latest_cost,
            "mcp_calls": dict(mcp_counts),
            "api_turns": num_turns_api,
            "turns": turns,
        }

    # Global stats
    all_turns = [e for e in entries if e["event"] == "turn_end"]
    all_usage = [e for e in entries if e["event"] == "usage_update"]
    total_cost = max((u.get("cost_usd", 0) for u in all_usage), default=0)

    date_range = f"{dates[0]}" if len(dates) == 1 else f"{dates[0]} → {dates[-1]}"

    return render_html(date_range, agent_summaries, entries, total_cost)

def fmt_tokens(n):
    if n >= 1000:
        return f"{n/1000:.1f}K"
    return str(n)

def fmt_duration(ms):
    if ms >= 60000:
        return f"{ms/60000:.1f}m"
    if ms >= 1000:
        return f"{ms/1000:.1f}s"
    return f"{ms}ms"

def fmt_cost(usd):
    if usd >= 1:
        return f"${usd:.2f}"
    if usd >= 0.01:
        return f"${usd:.3f}"
    return f"${usd:.4f}"

def render_html(date_range, summaries, entries, total_cost):
    sorted_agents = sorted(summaries.items(),
        key=lambda x: x[1]["total_token_delta"], reverse=True)

    # Build agent rows
    agent_rows = ""
    for agent, s in sorted_agents:
        nudge_pct = (s["nudge_token_delta"] / s["total_token_delta"] * 100) if s["total_token_delta"] > 0 else 0
        ctx_pct = (s["latest_context_used"] / s["latest_context_window"] * 100) if s["latest_context_window"] > 0 else 0

        mcp_str = ", ".join(f"{k.replace('mcp__pokegents-messaging__','')}: {v}"
                           for k, v in sorted(s["mcp_calls"].items(), key=lambda x: -x[1]))
        if not mcp_str:
            mcp_str = "—"

        agent_rows += f"""
        <tr>
            <td><strong>{agent}</strong><br><span class="dim">{s['pgid'][:8] if s['pgid'] else '—'} · {s['profile'] or '—'}</span></td>
            <td>{s['total_turns']}<br><span class="dim">{s['user_turns']}u + {s['nudge_turns']}n + {s['slash_turns']}s</span></td>
            <td class="num">{fmt_tokens(s['total_token_delta'])}<br><span class="dim nudge">{fmt_tokens(s['nudge_token_delta'])} nudge ({nudge_pct:.0f}%)</span></td>
            <td class="num">{fmt_tokens(s['latest_context_used'])} / {fmt_tokens(s['latest_context_window'])}<br>
                <div class="bar"><div class="fill" style="width:{ctx_pct:.0f}%"></div></div></td>
            <td class="num">{fmt_duration(s['total_duration_ms'])}<br><span class="dim">{fmt_duration(s['nudge_duration_ms'])} nudge</span></td>
            <td class="num">{fmt_cost(s['latest_cost_usd'])}</td>
            <td class="dim small">{mcp_str}</td>
        </tr>"""

    # Build turn-level detail (last 100 turns)
    recent_turns = sorted(
        [e for e in entries if e["event"] == "turn_end"],
        key=lambda e: e.get("ts", ""),
        reverse=True
    )[:200]

    turn_rows = ""
    for t in recent_turns:
        kind_class = "nudge" if t.get("prompt_kind") == "nudge" else ""
        err_class = "error" if t.get("error") else ""
        prompt = t.get("prompt", "")[:120]
        agent = t.get("agent") or t.get("pgid", "?")[:8]
        turn_rows += f"""
        <tr class="{kind_class} {err_class}">
            <td class="dim">{t.get('ts','')[:19]}</td>
            <td>{agent}</td>
            <td><span class="badge {t.get('prompt_kind','')}">{t.get('prompt_kind','?')}</span></td>
            <td class="num">{fmt_tokens(t.get('tokens_delta', 0))}</td>
            <td class="num">{fmt_duration(t.get('duration_ms', 0))}</td>
            <td class="prompt">{prompt}</td>
            <td class="dim">{t.get('error','') or '—'}</td>
        </tr>"""

    # Totals
    total_turns = sum(s["total_turns"] for s in summaries.values())
    total_nudge = sum(s["nudge_turns"] for s in summaries.values())
    total_delta = sum(s["total_token_delta"] for s in summaries.values())
    nudge_delta = sum(s["nudge_token_delta"] for s in summaries.values())
    nudge_pct_global = (nudge_delta / total_delta * 100) if total_delta > 0 else 0

    return f"""<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Pokegents Usage Report — {date_range}</title>
<style>
  * {{ margin: 0; padding: 0; box-sizing: border-box; }}
  body {{ font-family: -apple-system, 'SF Mono', monospace; background: #0a0f1a; color: #c8d0e0; padding: 24px; font-size: 13px; }}
  h1 {{ font-size: 18px; color: #e8ecf4; margin-bottom: 4px; }}
  h2 {{ font-size: 14px; color: #8090b0; margin: 24px 0 8px; text-transform: uppercase; letter-spacing: 1px; }}
  .summary {{ display: flex; gap: 24px; margin: 16px 0; }}
  .stat {{ background: rgba(255,255,255,0.05); border-radius: 8px; padding: 12px 16px; }}
  .stat .val {{ font-size: 22px; color: #e8ecf4; font-weight: 600; }}
  .stat .label {{ font-size: 11px; color: #6080a0; margin-top: 2px; }}
  table {{ width: 100%; border-collapse: collapse; margin: 8px 0; }}
  th {{ text-align: left; font-size: 10px; color: #5070a0; text-transform: uppercase; letter-spacing: 0.5px; padding: 6px 8px; border-bottom: 1px solid rgba(255,255,255,0.1); }}
  td {{ padding: 6px 8px; border-bottom: 1px solid rgba(255,255,255,0.04); vertical-align: top; }}
  .num {{ text-align: right; font-variant-numeric: tabular-nums; }}
  .dim {{ color: #506080; font-size: 11px; }}
  .small {{ font-size: 10px; }}
  .nudge {{ color: #d08040; }}
  .error {{ color: #e06060; }}
  .badge {{ display: inline-block; padding: 1px 6px; border-radius: 3px; font-size: 10px; }}
  .badge.user {{ background: rgba(60,120,200,0.2); color: #70a0e0; }}
  .badge.nudge {{ background: rgba(200,120,40,0.2); color: #d09050; }}
  .badge.slash {{ background: rgba(120,200,60,0.2); color: #90c060; }}
  .prompt {{ max-width: 400px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11px; }}
  .bar {{ width: 60px; height: 4px; background: rgba(255,255,255,0.1); border-radius: 2px; margin-top: 4px; }}
  .fill {{ height: 100%; background: #4080c0; border-radius: 2px; }}
  tr.nudge td {{ opacity: 0.7; }}
  #detail {{ display: none; }}
  .toggle {{ cursor: pointer; color: #5090d0; text-decoration: underline; margin: 8px 0; display: inline-block; }}
</style>
</head>
<body>

<h1>Pokegents Usage Report</h1>
<p class="dim">{date_range} · {len(summaries)} agents · {len(entries)} events</p>

<div class="summary">
    <div class="stat"><div class="val">{total_turns}</div><div class="label">Total turns</div></div>
    <div class="stat"><div class="val">{total_nudge}</div><div class="label">Nudge turns ({nudge_pct_global:.0f}%)</div></div>
    <div class="stat"><div class="val">{fmt_tokens(total_delta)}</div><div class="label">Token delta (all)</div></div>
    <div class="stat"><div class="val nudge">{fmt_tokens(nudge_delta)}</div><div class="label">Token delta (nudge)</div></div>
    <div class="stat"><div class="val">{fmt_cost(total_cost)}</div><div class="label">Total cost</div></div>
</div>

<h2>By Agent</h2>
<table>
<tr>
    <th>Agent</th>
    <th>Turns</th>
    <th>Token Δ</th>
    <th>Context</th>
    <th>Duration</th>
    <th>Cost</th>
    <th>MCP Calls</th>
</tr>
{agent_rows}
</table>

<h2><span class="toggle" onclick="document.getElementById('detail').style.display=document.getElementById('detail').style.display==='none'?'block':'none'">Turn Detail (click to expand)</span></h2>
<div id="detail">
<table>
<tr>
    <th>Time</th>
    <th>Agent</th>
    <th>Kind</th>
    <th>Token Δ</th>
    <th>Duration</th>
    <th>Prompt</th>
    <th>Error</th>
</tr>
{turn_rows}
</table>
</div>

</body>
</html>"""

if __name__ == "__main__":
    dates = resolve_dates(sys.argv[1:])
    entries = load_entries(dates)
    if not entries:
        print(f"No usage data found for {dates}. Log files live at {DATA_DIR}/", file=sys.stderr)
        print(f"Make sure the dashboard is running with the usage logger enabled.", file=sys.stderr)
        sys.exit(1)
    print(generate_report(entries, dates))
