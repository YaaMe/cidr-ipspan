#!/usr/bin/env bash
#
# Fetch published provider IP range lists and normalise them to one CIDR per
# line.
#
# These corpora exist because every performance conclusion in this repository
# used to rest on a single one (AWS), and the providers turn out to differ far
# more than that suggests: after merging, AWS collapses 87% and GitHub only
# 65%, which is the difference between a structure that suits the data and one
# that does not.
#
# The lists are published by each provider for exactly this use. They change,
# so the header of each file records where and when it came from; re-run this
# to refresh, and expect the numbers in bench/README.md to move.
#
# Azure is absent on purpose: its service tag list is behind an interstitial
# download page with a weekly-changing filename, so it cannot be fetched
# unattended. Add it by hand if you need it — it is the largest of the set and
# likely the most fragmented.

set -uo pipefail

cd "$(dirname "$0")/testdata" || exit 1

fetch() {
	local name=$1 url=$2 filter=$3
	local tmp
	tmp=$(mktemp)
	if ! curl -fsS --max-time 30 -o "$tmp" "$url"; then
		echo "FAILED  $name ($url)" >&2
		rm -f "$tmp"
		return 1
	fi
	{
		echo "# source: $url"
		echo "# fetched: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
		eval "$filter" < "$tmp" | sort -u
	} > "$name.cidr"
	rm -f "$tmp"
	printf '%-12s %6s prefixes\n' "$name" "$(grep -cv '^#' "$name.cidr")"
}

# JSON lists keyed by a field name.
json_field() { grep -oE "\"$1\": *\"[^\"]+\"" | grep -oE '[0-9a-fA-F:.]+/[0-9]+'; }
# Bare CIDR per line, or CIDR as the first CSV column.
bare()     { grep -oE '^[0-9a-fA-F:.]+/[0-9]+'; }
csv_first(){ cut -d, -f1 | grep -oE '^[0-9a-fA-F:.]+/[0-9]+'; }
# Any quoted CIDR anywhere in the document.
quoted()   { grep -oE '"[0-9a-fA-F:.]+/[0-9]+"' | tr -d '"'; }

fetch aws        https://ip-ranges.amazonaws.com/ip-ranges.json          "grep -oE '\"(ip_prefix|ipv6_prefix)\": *\"[^\"]+\"' | grep -oE '[0-9a-fA-F:.]+/[0-9]+'"
fetch gcp        https://www.gstatic.com/ipranges/cloud.json             "grep -oE '\"(ipv4Prefix|ipv6Prefix)\": *\"[^\"]+\"' | grep -oE '[0-9a-fA-F:.]+/[0-9]+'"
fetch cloudflare https://www.cloudflare.com/ips-v4                       bare
curl -fsS --max-time 30 https://www.cloudflare.com/ips-v6 2>/dev/null \
	| grep -oE '^[0-9a-fA-F:.]+/[0-9]+' >> cloudflare.cidr
fetch fastly     https://api.fastly.com/public-ip-list                   quoted
fetch github     https://api.github.com/meta                             quoted
fetch oracle     https://docs.oracle.com/en-us/iaas/tools/public_ip_ranges.json "json_field cidr"
fetch digitalocean https://www.digitalocean.com/geo/google.csv           csv_first
fetch linode     https://geoip.linode.com/                               bare
