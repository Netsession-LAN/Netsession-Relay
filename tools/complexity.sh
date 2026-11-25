#!/bin/sh
# Complexity limits of the Go code (CONTRIBUTING.md, "Code shape"): golangci-lint checks every function (gocyclo,
# at most 7); this script checks every file: the cyclomatic complexity of its functions sums up to at most 37.
# Test files and test data do not count. Usage (repo root): sh tools/complexity.sh
set -eu
limit=${FILE_COMPLEXITY_LIMIT:-37}
over=$(go run github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0 -ignore '_test\.go|testdata' . |
  awk -v limit="$limit" '{ split($4, at, ":"); sum[at[1]] += $1 }
    END { for (f in sum) if (sum[f] > limit) printf "%4d  %s\n", sum[f], f }' | sort -rn)
if [ -n "$over" ]; then
  echo "files above a complexity of $limit (split them along their responsibilities):"
  echo "$over"
  exit 1
fi
echo "file complexity: every file at most $limit"
