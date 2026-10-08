#!/bin/bash
# notify.sh — send one operational alert to the platform Telegram chat.
#
# Why this is a shell script and not the application: the alert has to fire when
# the application is NOT running (crash, failed restart, /ready down), so it cannot
# live inside the process it reports on. It reads the same credentials the app does,
# from the same file, so there is exactly one place to rotate them.
#
# Usage: notify.sh "message"
set -euo pipefail

ENV_FILE=${ENV_FILE:-/opt/khmer-ai-cs/.env-go}
if [ ! -r "$ENV_FILE" ]; then
  echo "notify.sh: $ENV_FILE is not readable" >&2
  exit 1
fi
set -a
# shellcheck disable=SC1090,SC1091  # a runtime secret store, not a source file
. "$ENV_FILE"
set +a

TOKEN=${PLATFORM_TELEGRAM_BOT_TOKEN:-}
# Same fallback the app uses: with no alert chat configured, the first admin id.
CHAT=${PLATFORM_TELEGRAM_ALERT_CHAT:-${PLATFORM_TELEGRAM_ADMINS%%,*}}
if [ -z "$TOKEN" ] || [ -z "$CHAT" ]; then
  echo "notify.sh: set PLATFORM_TELEGRAM_BOT_TOKEN and PLATFORM_TELEGRAM_ALERT_CHAT" >&2
  exit 2
fi

TEXT=$(printf '[%s] %s' "$(hostname)" "${1:-(no message)}")
# The token travels in the URL path, so the response body (and any -x trace) is
# discarded on purpose: an alerting tool must not be the way a credential leaks.
code=$(curl -sS --max-time 10 -o /dev/null -w '%{http_code}' \
  "https://api.telegram.org/bot${TOKEN}/sendMessage" \
  --data-urlencode "chat_id=${CHAT}" \
  --data-urlencode "text=${TEXT}") || code=000
echo "notify.sh: telegram http ${code}"
[ "$code" = "200" ]
