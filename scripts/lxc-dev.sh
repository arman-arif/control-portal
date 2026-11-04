#!/usr/bin/env bash
# Local test environment built from unprivileged LXC containers:
#   keel-db         Ubuntu 24.04 with PostgreSQL (databases keel and keel_test)
#   keel-vps-noble  Ubuntu 24.04, a stand-in VPS to link to the panel
#   keel-vps-jammy  Ubuntu 22.04, a stand-in VPS to link to the panel
#   keel-pebble     Ubuntu 24.04 running Pebble, a test ACME server standing
#                   in for Let's Encrypt (optional: `pebble` creates it)
#
# Usage: scripts/lxc-dev.sh up | down | reset | env | link NAME 'INSTALL COMMAND'
#        scripts/lxc-dev.sh pebble | acme-host VPS DOMAIN...
#
# For HTTPS tests, run `pebble`, set KEEL_ACME_SERVER from `env`, and point
# each test domain at its VPS with `acme-host`, for example
#   scripts/lxc-dev.sh acme-host keel-vps-noble shop.keel.test www.shop.keel.test
# Pebble checks challenges for real over port 80 of that VPS. Its
# certificates come from a throwaway root, so browsers and curl need -k.
#
# Needs classic LXC (lxc-utils) set up for unprivileged containers, once:
#   /etc/lxc/lxc-usernet         $USER veth lxcbr0 10
#   ~/.config/lxc/default.conf   network on lxcbr0, lxc.idmap for your
#                                /etc/subuid and /etc/subgid range,
#                                lxc.apparmor.profile = unconfined
# The containers' root must be able to traverse to ~/.local/share/lxc, e.g.
#   setfacl -m u:100000:x ~ ~/.local ~/.local/share
#
# Run the panel on the host with KEEL_PUBLIC_URL set to the lxcbr0 address
# (`env` prints it) so the VPS containers can reach it.
set -euo pipefail

DB=keel-db
PEBBLE=keel-pebble
VPSES=(keel-vps-noble:noble keel-vps-jammy:jammy)
BRIDGE=lxcbr0

exists() { lxc-info -n "$1" >/dev/null 2>&1; }
running() { [ "$(lxc-info -n "$1" -sH 2>/dev/null)" = RUNNING ]; }

ip_of() {
	local ip
	for _ in $(seq 30); do
		ip=$(lxc-info -n "$1" -iH 2>/dev/null | grep -E '^10\.|^192\.168\.|^172\.' | head -1 || true)
		[ -n "$ip" ] && { echo "$ip"; return; }
		sleep 1
	done
	echo "keel: $1 got no IPv4 address" >&2
	return 1
}

ensure() { # name release
	if ! exists "$1"; then
		echo "keel: creating $1 (ubuntu $2)"
		lxc-create -t download -n "$1" -- -d ubuntu -r "$2" -a amd64 >/dev/null
	fi
	running "$1" || lxc-unpriv-start -n "$1" >/dev/null
	ip_of "$1" >/dev/null
}

setup_db() {
	lxc-attach -n "$DB" -- bash -euc '
		command -v psql >/dev/null && exit 0
		echo "keel: installing PostgreSQL in keel-db"
		export DEBIAN_FRONTEND=noninteractive
		apt-get update -qq && apt-get install -y -qq postgresql >/dev/null
		conf=$(ls -d /etc/postgresql/*/main)
		echo "listen_addresses = '"'"'*'"'"'" >>"$conf/postgresql.conf"
		echo "host all all 10.0.0.0/8 scram-sha-256" >>"$conf/pg_hba.conf"
		systemctl restart postgresql
		sudo -u postgres psql -qc "CREATE ROLE keel LOGIN PASSWORD '"'"'keel'"'"' CREATEDB;"
		sudo -u postgres createdb -O keel keel
		sudo -u postgres createdb -O keel keel_test'
}

cmd_env() {
	local db host
	db=$(ip_of "$DB")
	host=$(ip -4 -br addr show "$BRIDGE" | awk '{print $3}' | cut -d/ -f1)
	cat <<EOF
export KEEL_DATABASE_URL=postgres://keel:keel@$db:5432/keel?sslmode=disable
export KEEL_TEST_DATABASE_URL=postgres://keel:keel@$db:5432/keel_test?sslmode=disable
export KEEL_PUBLIC_URL=http://$host:8080
EOF
	if exists "$PEBBLE" && running "$PEBBLE"; then
		echo "export KEEL_ACME_SERVER=https://$(ip_of "$PEBBLE"):14000/dir"
	fi
}

cmd_pebble() {
	ensure "$PEBBLE" noble
	local ip
	ip=$(ip_of "$PEBBLE")
	lxc-attach -n "$PEBBLE" -- env IP="$ip" bash -euc '
		export DEBIAN_FRONTEND=noninteractive
		command -v pebble >/dev/null || { echo "keel: installing Pebble in keel-pebble"; apt-get update -qq && apt-get install -y -qq pebble openssl >/dev/null; }
		mkdir -p /etc/keel-pebble && cd /etc/keel-pebble
		if [ ! -f ca.pem ] || ! grep -q "IP:$IP," san.txt 2>/dev/null; then
			# A CA the VPSes trust, and a certificate for the ACME endpoint itself.
			openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
				-subj "/CN=Keel test ACME CA" -keyout ca.key -out ca.pem 2>/dev/null
			echo "subjectAltName=IP:$IP,DNS:keel-pebble" >san.txt
			openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -subj "/CN=keel-pebble" \
				-keyout server.key -out server.csr 2>/dev/null
			openssl x509 -req -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 3650 \
				-extfile san.txt -out server.pem 2>/dev/null
		fi
		cat >config.json <<JSON
{"pebble": {"listenAddress": "0.0.0.0:14000", "managementListenAddress": "0.0.0.0:15000",
  "certificate": "/etc/keel-pebble/server.pem", "privateKey": "/etc/keel-pebble/server.key",
  "httpPort": 80, "tlsPort": 443, "ocspResponderURL": "", "externalAccountBindingRequired": false}}
JSON
		cat >/etc/systemd/system/keel-pebble.service <<UNIT
[Unit]
Description=Pebble test ACME server for Keel
After=network-online.target
[Service]
Environment=PEBBLE_VA_NOSLEEP=1 PEBBLE_WFE_NONCEREJECT=0
ExecStart=/usr/bin/pebble -config /etc/keel-pebble/config.json
Restart=always
[Install]
WantedBy=multi-user.target
UNIT
		systemctl daemon-reload
		systemctl enable keel-pebble >/dev/null 2>&1
		systemctl restart keel-pebble'
	local ca
	ca=$(lxc-attach -n "$PEBBLE" -- cat /etc/keel-pebble/ca.pem)
	for v in "${VPSES[@]%%:*}"; do
		running "$v" || continue
		printf '%s\n' "$ca" | lxc-attach -n "$v" -- bash -c 'cat >/usr/local/share/ca-certificates/keel-pebble.crt && update-ca-certificates >/dev/null'
		echo "keel: $v trusts the Pebble endpoint"
	done
	echo "export KEEL_ACME_SERVER=https://$ip:14000/dir"
}

# cmd_acme_host makes Pebble resolve the domains to a VPS, replacing any
# earlier entry for them.
cmd_acme_host() { # vps domain...
	[ $# -ge 2 ] || { echo "usage: $0 acme-host VPS DOMAIN..." >&2; exit 2; }
	local vps=$1 ip
	shift
	ip=$(ip_of "$vps")
	lxc-attach -n "$PEBBLE" -- env IP="$ip" bash -euc '
		for d in "$@"; do
			grep -vE "[[:space:]]$d([[:space:]]|\$)" /etc/hosts >/etc/hosts.keel || true
			cat /etc/hosts.keel >/etc/hosts
			echo "$IP $d" >>/etc/hosts
		done' _ "$@"
	echo "keel: Pebble resolves $* to $vps ($ip)"
}

cmd_up() {
	ensure "$DB" noble
	setup_db
	for v in "${VPSES[@]}"; do ensure "${v%%:*}" "${v##*:}"; done
	echo
	echo "# Panel settings (eval \"\$(scripts/lxc-dev.sh env)\"):"
	cmd_env
	echo
	echo "# Then add a server in the panel and run: scripts/lxc-dev.sh link keel-vps-noble '<install command>'"
}

cmd_down() {
	for n in "$DB" "$PEBBLE" "${VPSES[@]%%:*}"; do
		running "$n" && lxc-stop -n "$n" && echo "keel: stopped $n"
	done
	return 0
}

cmd_reset() {
	for n in "${VPSES[@]%%:*}"; do
		exists "$n" && lxc-destroy -f -n "$n" >/dev/null && echo "keel: destroyed $n"
	done
	echo "keel: $DB kept (data intact); destroy it with: lxc-destroy -f -n $DB"
}

cmd_link() { # name 'curl ... | sudo bash -s -- TOKEN'
	[ $# -eq 2 ] || { echo "usage: $0 link NAME 'INSTALL COMMAND'" >&2; exit 2; }
	lxc-attach -n "$1" -- bash -c "command -v curl >/dev/null || { apt-get update -qq && apt-get install -y -qq curl >/dev/null; }; $2"
}

case "${1:-}" in
up) cmd_up ;;
down) cmd_down ;;
reset) cmd_reset ;;
env) cmd_env ;;
link) shift; cmd_link "$@" ;;
pebble) cmd_pebble ;;
acme-host) shift; cmd_acme_host "$@" ;;
*) echo "usage: $0 up | down | reset | env | link NAME 'INSTALL COMMAND' | pebble | acme-host VPS DOMAIN..." >&2; exit 2 ;;
esac
