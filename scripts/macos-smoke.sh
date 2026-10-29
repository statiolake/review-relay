#!/bin/sh
# Starts the resident macOS app for real and drives it from the command line:
# detached start, the agent CLI, focus on a second invocation, --wait, quit
# and comments surviving a restart. Needs a GUI session (CI's macOS runners
# have one).
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
bin="$work/review-relay"
(cd "$root" && go build -o "$bin" .)
log="${TMPDIR:-/tmp}"
log="${log%/}/review-relay-$(id -u)/app.log"

quit_app() {
	pkill -TERM -f -- "$bin --resident-app" || true
	for _ in $(seq 50); do
		pgrep -f -- "$bin --resident-app" >/dev/null || return 0
		sleep 0.1
	done
	echo "the app did not quit" >&2
	return 1
}
trap 'quit_app; echo "--- app.log"; cat "$log" 2>/dev/null || true' EXIT

repo="$work/repo"
mkdir "$repo"
cd "$repo"
git init -q
echo hello >a.txt

step() { echo "==> $*"; }

step "first invocation starts the app and returns"
"$bin" HEAD -- 2>&1 || true # no commits yet: an error comes back from the app
"$bin"
pgrep -f -- "$bin --resident-app" >/dev/null

step "agents find the review"
"$bin" health
"$bin" comments add --path a.txt --line 0 --body "smoke comment" >/dev/null

step "second invocation focuses the open window"
"$bin"
test "$(pgrep -f -- "$bin --resident-app" | wc -l | tr -d ' ')" = 1

step "--wait returns 0 when the app quits"
"$bin" --wait &
waiter=$!
sleep 1
kill -0 "$waiter" # still waiting
quit_app
wait "$waiter"
if "$bin" health 2>/dev/null; then
	echo "the review is still registered" >&2
	exit 1
fi

step "comments survive a restart"
"$bin"
"$bin" comments list | grep -q "smoke comment"

echo "ok"
