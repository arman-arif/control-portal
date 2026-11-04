#!/usr/bin/env bash
# Local test environment built from unprivileged LXC containers:
#   keel-db         Ubuntu 24.04 with PostgreSQL (databases keel and keel_test)
#   keel-vps-noble  Ubuntu 24.04, a stand-in VPS to link to the panel
#   keel-vps-jammy  Ubuntu 22.04, a stand-in VPS to link to the panel
#
# Usage: scripts/lxc-dev.sh up | down | reset | env | link NAME 'INSTALL COMMAND'
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
	for n in "$DB" "${VPSES[@]%%:*}"; do
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
*) echo "usage: $0 up | down | reset | env | link NAME 'INSTALL COMMAND'" >&2; exit 2 ;;
esac
