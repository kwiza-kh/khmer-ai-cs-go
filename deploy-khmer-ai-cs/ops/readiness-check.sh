#!/bin/bash
# readiness-check.sh — probe the backend's /ready and alert when it stops answering.
#
# Why this exists: the daily digest is sent BY the application, so a dead
# application cannot report its own death; Cloudflare answers from cache and the
# frontend keeps serving its own pages, so nothing else notices either. This runs
# from systemd, outside the process being watched.
#
# One alert per outage and one on recovery, not one per timer tick: a state file
# remembers which side of the edge we are on.
set -uo pipefail

URL=${URL:-http://127.0.0.1:8081/ready}
STATE=${STATE:-/opt/khmer-ai-cs/ops/readiness.state}

state=ok
[ -r "$STATE" ] && state=$(cat "$STATE")

body=$(curl -sS --max-time 10 "$URL" 2>/dev/null)
code=$?

if [ $code -eq 0 ] && printf '%s' "$body" | grep -q '"status":"ok"'; then
  if [ "$state" != "ok" ]; then
    /opt/khmer-ai-cs/ops/notify.sh "后端已恢复：GET ${URL} 正常（${body}）" || true
    echo ok >"$STATE"
  fi
  exit 0
fi

# Still down. Only the transition is announced.
if [ "$state" = "ok" ]; then
  /opt/khmer-ai-cs/ops/notify.sh "后端不可用：GET ${URL} 失败（curl=${code}, body=${body:0:140}）" || true
  echo down >"$STATE"
fi
exit 0
