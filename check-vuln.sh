#!/bin/sh
set -e

# Check for vulnerable dependencies using govulncheck and attempt to auto-fix
# by upgrading modules to their fixed versions.

# Require jq for JSON parsing.
if ! command -v jq >/dev/null 2>&1; then
	echo "ERROR: jq is required but not installed." >&2
	exit 1
fi

# Require govulncheck.
if ! command -v govulncheck >/dev/null 2>&1; then
	echo "ERROR: govulncheck is required but not installed." >&2
	echo "Install with: go install golang.org/x/vuln/cmd/govulncheck@latest" >&2
	exit 1
fi

VULN_OUTPUT="vulncheck-output.json"
VULN_RECHECK="vulncheck-recheck.json"

cleanup() {
	rm -f "$VULN_OUTPUT" "$VULN_RECHECK" go-get-errors.tmp
}
trap cleanup EXIT

# Ensure vendor directory is consistent before scanning.
if [ -d vendor ]; then
	echo "Syncing vendor directory..."
	make govendor
fi

echo "Running govulncheck..."
govulncheck -json ./... > "$VULN_OUTPUT" 2>/dev/null

# Extract findings that have a fixed_version (actionable vulnerabilities).
# Each line of the JSON stream is a Message object; filter for those with a "finding" field.

# Log all findings summary: OSV ID, module, fixed_version.
echo ""
echo "=== Vulnerability scan results ==="
FINDINGS_SUMMARY=$(jq -r '
	select(.finding != null) |
	"\(.finding.osv)\t\(.finding.trace[0].module)\t\(.finding.fixed_version // "NO FIX")"
' "$VULN_OUTPUT" | sort -u)

if [ -n "$FINDINGS_SUMMARY" ]; then
	printf "%-20s %-40s %s\n" "OSV ID" "MODULE" "FIXED VERSION"
	printf "%-20s %-40s %s\n" "------" "------" "-------------"
	echo "$FINDINGS_SUMMARY" | while IFS='	' read -r osv mod fixed; do
		printf "%-20s %-40s %s\n" "$osv" "$mod" "$fixed"
	done
else
	echo "No vulnerabilities found."
	exit 0
fi
echo ""

# Module vulnerabilities (fixable via go get module@version).
MODULE_FIXES=$(jq -r '
	select(.finding != null) |
	select(.finding.fixed_version != null) |
	select(.finding.fixed_version != "") |
	select(.finding.trace[0].module != "stdlib") |
	"\(.finding.trace[0].module)@\(.finding.fixed_version)"
' "$VULN_OUTPUT" | sort -u)

# Stdlib vulnerabilities (fixable via go get toolchain@version).
STDLIB_FIXES=$(jq -r '
	select(.finding != null) |
	select(.finding.fixed_version != null) |
	select(.finding.fixed_version != "") |
	select(.finding.trace[0].module == "stdlib") |
	.finding.fixed_version
' "$VULN_OUTPUT" | sort -Vu | tail -n 1)

if [ -z "$MODULE_FIXES" ] && [ -z "$STDLIB_FIXES" ]; then
	# Findings exist but none are fixable.
	UNFIXABLE=$(jq -r '
		select(.finding != null) |
		select(.finding.fixed_version == null or .finding.fixed_version == "") |
		.finding.osv
	' "$VULN_OUTPUT" | sort -u)

	if [ -n "$UNFIXABLE" ]; then
		echo "WARNING: Found vulnerabilities with no available fix:"
		echo "$UNFIXABLE"
	fi

	echo "ERROR: Unable to resolve vulnerabilities"
	exit 1
fi

echo "Found vulnerable dependencies. Attempting to fix..."
echo ""

# Log current module versions before upgrade.
echo "=== Current module versions (before fix) ==="
for fix in $MODULE_FIXES; do
	mod=$(echo "$fix" | cut -d@ -f1)
	current=$(go list -m "$mod" 2>/dev/null | awk '{print $2}')
	target=$(echo "$fix" | cut -d@ -f2)
	echo "  $mod: $current -> $target"
done
echo ""

# Upgrade stdlib via toolchain directive if needed.
# govulncheck outputs versions as "v1.X.Y" but go get toolchain@ expects "go1.X.Y".
if [ -n "$STDLIB_FIXES" ]; then
	TOOLCHAIN_VERSION=$(echo "$STDLIB_FIXES" | sed 's/^v/go/')
	echo "  go get toolchain@$TOOLCHAIN_VERSION"
	go get "toolchain@$TOOLCHAIN_VERSION"
fi

# Apply module fixes in a single go get invocation so Go's MVS resolves all
# constraints together, preventing sequential upgrades from downgrading each other.
# Deduplicate by module, keeping only the highest required version per module.
DEDUPED_FIXES=$(echo "$MODULE_FIXES" | awk -F'@' '
	{
		mod = $1
		ver = $2
		if (!(mod in best) || ver > best[mod]) {
			best[mod] = ver
		}
	}
	END {
		for (mod in best) print mod "@" best[mod]
	}
' | sort)

echo "  go get $DEDUPED_FIXES"
# shellcheck disable=SC2086
if ! go get $DEDUPED_FIXES 2>go-get-errors.tmp; then
	# When go get fails with "requires pkg@vX, not pkg@vY", it means another
	# module in our list transitively requires a HIGHER version of that package.
	# Since the higher version still satisfies the vulnerability fix (fixes are
	# minimum versions), we can safely drop the conflicting lower pins and retry.
	CONFLICTING=$(grep -o 'not [^ ]*' go-get-errors.tmp | awk '{print $2}' | cut -d@ -f1 | sort -u)
	if [ -n "$CONFLICTING" ]; then
		echo ""
		echo "  Version conflict detected. Dropping modules satisfied transitively:"
		for cmod in $CONFLICTING; do
			echo "    $cmod (will be pulled to a higher version by transitive deps)"
		done
		FILTERED_FIXES=""
		for fix in $DEDUPED_FIXES; do
			mod=$(echo "$fix" | cut -d@ -f1)
			if ! echo "$CONFLICTING" | grep -qx "$mod"; then
				FILTERED_FIXES="$FILTERED_FIXES $fix"
			fi
		done
		echo ""
		echo "  go get $FILTERED_FIXES"
		# shellcheck disable=SC2086
		go get $FILTERED_FIXES
	else
		echo "  go get failed:"
		cat go-get-errors.tmp
		rm -f go-get-errors.tmp
		exit 1
	fi
	rm -f go-get-errors.tmp
else
	rm -f go-get-errors.tmp
fi

echo ""
echo "Running make govendor..."
make govendor

echo "Verifying build..."
if ! go build ./...; then
	echo ""
	echo "ERROR: Unable to resolve vulnerabilities (build failed after upgrade)"
	exit 1
fi

echo "Re-running govulncheck to verify fix..."
govulncheck -json ./... > "$VULN_RECHECK" 2>/dev/null

REMAINING=$(jq -r '
	select(.finding != null) |
	.finding.osv
' "$VULN_RECHECK" | sort -u)

if [ -n "$REMAINING" ]; then
	echo ""
	echo "=== Remaining vulnerabilities after fix attempt ==="

	# Show fixable remaining (have a fixed_version).
	REMAINING_FIXABLE=$(jq -r '
		select(.finding != null) |
		select(.finding.fixed_version != null) |
		select(.finding.fixed_version != "") |
		"\(.finding.osv)\t\(.finding.trace[0].module)\t\(.finding.fixed_version)"
	' "$VULN_RECHECK" | sort -u)

	# Show unfixable remaining (no fixed_version).
	REMAINING_UNFIXABLE=$(jq -r '
		select(.finding != null) |
		select(.finding.fixed_version == null or .finding.fixed_version == "") |
		"\(.finding.osv)\t\(.finding.trace[0].module)\tNO FIX AVAILABLE"
	' "$VULN_RECHECK" | sort -u)

	if [ -n "$REMAINING_FIXABLE" ]; then
		echo ""
		echo "Fixable (upgrade may have been blocked by dependency constraints):"
		printf "  %-20s %-40s %s\n" "OSV ID" "MODULE" "REQUIRED VERSION"
		echo "$REMAINING_FIXABLE" | while IFS='	' read -r osv mod fixed; do
			printf "  %-20s %-40s %s\n" "$osv" "$mod" "$fixed"
		done
	fi

	if [ -n "$REMAINING_UNFIXABLE" ]; then
		echo ""
		echo "No fix available (waiting on upstream patch):"
		printf "  %-20s %-40s %s\n" "OSV ID" "MODULE" "STATUS"
		echo "$REMAINING_UNFIXABLE" | while IFS='	' read -r osv mod status; do
			printf "  %-20s %-40s %s\n" "$osv" "$mod" "$status"
		done
	fi

	echo ""
	# Log actual module versions after fix attempt for debugging.
	echo "=== Current module versions (after fix) ==="
	REMAINING_MODS=$(jq -r '
		select(.finding != null) |
		.finding.trace[0].module
	' "$VULN_RECHECK" | sort -u | grep -v "^stdlib$" || true)
	for mod in $REMAINING_MODS; do
		current=$(go list -m "$mod" 2>/dev/null | awk '{print $2}')
		echo "  $mod: $current"
	done
	echo ""

	# TODO: Invoke copilot from the CLI to attempt to resolve remaining issues.
	#   e.g. copilot-cli fix-vulns --input "$VULN_RECHECK"
	echo "ERROR: Unable to resolve vulnerabilities"
	exit 1
fi

echo "All vulnerabilities resolved successfully."
exit 0
