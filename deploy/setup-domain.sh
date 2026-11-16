#!/usr/bin/env bash
#
# Put papaya on your own domain.
#
# Two things have to be true, and they are independent:
#
#   1. The name resolves to the node's tailnet address. The record is public but
#      the address is CGNAT, so only your tailnet can route to it. It must be
#      "DNS only" — a proxied record would make Cloudflare try to reach 100.x
#      from its edge, which cannot work.
#
#   2. A certificate exists for that name. Tailscale only issues for *.ts.net,
#      and the node is not reachable from the internet, so the challenge has to
#      be DNS-01. HTTP-01 would require Let's Encrypt to connect to you.
#
# Usage:
#   deploy/setup-domain.sh term.example.com [node-name]
#
# The node name defaults to $PAPAYA_HOSTNAME, then to "papaya". Pass it when the
# node is named something else — running a second instance means a second name,
# since reusing one silently gets you "papaya-1".
#
# Needs deploy/.cloudflare containing:
#   CLOUDFLARE_DNS_API_TOKEN=...      # Zone:DNS:Edit on the zone
set -euo pipefail

FQDN="${1:?usage: setup-domain.sh <fqdn> [node]   e.g. term.example.com my-papaya}"
ZONE="${FQDN#*.}"                       # term.example.com -> example.com
NODE="${2:-${PAPAYA_HOSTNAME:-papaya}}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CERTS="$ROOT/deploy/certs"

# shellcheck disable=SC1091
[ -f "$ROOT/deploy/.cloudflare" ] || { echo "missing deploy/.cloudflare"; exit 1; }
set -a; . "$ROOT/deploy/.cloudflare"; set +a
: "${CLOUDFLARE_DNS_API_TOKEN:?set CLOUDFLARE_DNS_API_TOKEN in deploy/.cloudflare}"

EMAIL="${ACME_EMAIL:?set ACME_EMAIL to the address Let's Encrypt should use}"
API="https://api.cloudflare.com/client/v4"
auth=(-H "Authorization: Bearer $CLOUDFLARE_DNS_API_TOKEN" -H "Content-Type: application/json")

say() { printf '\n\033[1m%s\033[0m\n' "$*"; }

STATUS="$(tailscale status --json)"

# Match on the DNS label as well as HostName, and prefer a node that is online.
#
# Re-registering leaves the previous node behind holding the original name, so
# the new one becomes "<name>-1" — but both keep reporting the *same* HostName.
# HostName alone therefore cannot tell them apart, and picking whichever came
# first in the map would point the domain at a dead address roughly half the
# time. The DNS label is unique per node, and only one of the pair is online.
TS_IP="$(printf '%s' "$STATUS" | NODE="$NODE" python3 -c '
import json,os,sys
want=os.environ["NODE"].lower()
d=json.load(sys.stdin)
nodes=list((d.get("Peer") or {}).values())
if d.get("Self"): nodes.append(d["Self"])

def names(p):
    out = set()
    h = (p.get("HostName") or "").lower()
    if h: out.add(h)
    dns = (p.get("DNSName") or "").strip(".").lower()
    if dns: out.add(dns.split(".")[0])
    return out

matches = [p for p in nodes if want in names(p)]
online  = [p for p in matches if p.get("Online")]
pick    = online or matches
if len(pick) == 1:
    print(pick[0]["TailscaleIPs"][0])
')"
if [ -z "$TS_IP" ]; then
  echo "could not resolve exactly one node named \"$NODE\" on the tailnet."
  echo "candidates:"
  printf '%s' "$STATUS" | python3 -c '
import json,sys
d=json.load(sys.stdin)
nodes=list((d.get("Peer") or {}).values())
if d.get("Self"): nodes.append(d["Self"])
for p in sorted(nodes, key=lambda p: (p.get("DNSName") or "")):
    if "papaya" in ((p.get("HostName") or "") + (p.get("DNSName") or "")):
        print("   %-26s %-16s %s" % ((p.get("DNSName") or "").strip(".").split(".")[0],
                                     p["TailscaleIPs"][0],
                                     "online" if p.get("Online") else "offline"))
'
  echo "pass the right one:  deploy/setup-domain.sh $FQDN <node>"
  exit 1
fi

say "1. DNS  $FQDN -> $TS_IP (DNS only, not proxied)"

ZONE_ID="$(curl -sS "${auth[@]}" "$API/zones?name=$ZONE" \
  | python3 -c 'import json,sys; r=json.load(sys.stdin)["result"]; print(r[0]["id"] if r else "")')"
[ -n "$ZONE_ID" ] || { echo "   zone $ZONE not found — is the token scoped to it?"; exit 1; }

REC_ID="$(curl -sS "${auth[@]}" "$API/zones/$ZONE_ID/dns_records?name=$FQDN&type=A" \
  | python3 -c 'import json,sys; r=json.load(sys.stdin)["result"]; print(r[0]["id"] if r else "")')"

# proxied=false is essential: Cloudflare's edge cannot reach a 100.x address.
BODY="$(python3 -c "
import json,sys
print(json.dumps({'type':'A','name':'$FQDN','content':'$TS_IP','ttl':60,'proxied':False}))")"

if [ -n "$REC_ID" ]; then
  curl -sS -X PUT "${auth[@]}" "$API/zones/$ZONE_ID/dns_records/$REC_ID" -d "$BODY" \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); print("   updated" if d["success"] else "   FAILED: "+json.dumps(d["errors"]))'
else
  curl -sS -X POST "${auth[@]}" "$API/zones/$ZONE_ID/dns_records" -d "$BODY" \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); print("   created" if d["success"] else "   FAILED: "+json.dumps(d["errors"]))'
fi

say "2. Certificate for $FQDN via DNS-01"
mkdir -p "$CERTS"

# lego 5 merged "renew" into "run": it issues when there is no certificate and
# renews when one is near expiry, so there is nothing to branch on. Calling
# "lego renew --days 30" — which earlier versions took — now fails with "No help
# topic for 'renew'", and under set -e that aborted the script before the
# permission fix below could run. The renewal then failed silently every month.

# The system resolver on a tailnet is MagicDNS (100.100.100.100), which answers
# NXDOMAIN for public records — including lego's own _acme-challenge TXT. The
# propagation check has to ask public resolvers instead.
LEGO_DNS_RESOLVERS="${LEGO_DNS_RESOLVERS:-1.1.1.1:53,8.8.8.8:53}" \
LEGO_PATH="$CERTS" \
LEGO_ACCEPT_TOS=true \
LEGO_EMAIL="$EMAIL" \
LEGO_DNS=cloudflare \
LEGO_DOMAINS="$FQDN" \
LEGO_RENEW_DAYS="${LEGO_RENEW_DAYS:-30}" \
CLOUDFLARE_DNS_API_TOKEN="$CLOUDFLARE_DNS_API_TOKEN" \
  lego run

chmod 600 "$CERTS/certificates/$FQDN.key" 2>/dev/null || true

# In Docker the process runs under its own uid and cannot read material owned by
# the account that issued it. lego resets these files to 0600 on every renewal,
# so opening them up belongs here rather than in a one-off setup step —
# otherwise TLS works until the first renewal and then the container refuses to
# start, a month later, with nobody watching.
#
# Only certificates/ is opened. accounts/ holds the ACME account key, which the
# server has no business reading.
if [ -n "${PAPAYA_CERT_GROUP:-}" ]; then
  say "3. Granting group $PAPAYA_CERT_GROUP read access, for the container"
  if ! sudo -n chgrp "$PAPAYA_CERT_GROUP" "$CERTS" "$CERTS/certificates" "$CERTS"/certificates/*; then
    echo "   FAILED: could not chgrp to $PAPAYA_CERT_GROUP." >&2
    echo "   The certificate renewed, but the container will not be able to read it." >&2
    exit 1
  fi
  sudo -n chmod 750 "$CERTS" "$CERTS/certificates"
  sudo -n chmod 640 "$CERTS"/certificates/*
  echo "   done"
fi

say "4. Add to .env, then restart"
cat <<EOF
  PAPAYA_TLS=true

  Running natively:
  PAPAYA_TLS_CERT=$CERTS/certificates/$FQDN.crt
  PAPAYA_TLS_KEY=$CERTS/certificates/$FQDN.key

  Running under compose, which mounts deploy/ at /etc/papaya and runs as uid
  10001 (set PAPAYA_CERT_GROUP=10001 when running this script, including in the
  renewal cron, or the container cannot read the key):
  PAPAYA_TLS_CERT_CONTAINER=/etc/papaya/certs/certificates/$FQDN.crt
  PAPAYA_TLS_KEY_CONTAINER=/etc/papaya/certs/certificates/$FQDN.key

  Renewal: re-run this script. papaya re-reads the files, so no restart is
  needed for a renewal — only for first enabling them.
EOF
