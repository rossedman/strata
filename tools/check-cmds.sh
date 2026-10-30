#!/usr/bin/env sh
# fail if any cmd/*.go (non-test) defines != 1 cobra.Command
fail=0
for f in cmd/*.go; do
  case "$f" in *_test.go) continue;; esac
  n=$(grep -c '&cobra.Command{' "$f")
  [ "$n" -ne 1 ] && echo "$f defines $n commands" && fail=1
done
exit $fail
