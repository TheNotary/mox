#!/bin/sh
set -x # print commands
set -e # exit on failed command

# This script sets up mox in "direct-bind mode": running as a non-root user
# without the root->fork->child flow. All ports must be >1024 since we can't
# bind privileged ports. This tests the code path in serve_unix.go where
# MOX_SOCKETS is NOT set and uid != 0.

(rm -r /tmp/mox 2>/dev/null || exit 0) # clean slate
mkdir /tmp/mox
cd /tmp/mox
mkdir -p config data

# Write a minimal mox.conf with all ports >1024 for unprivileged binding.
cat > config/mox.conf <<'MOXCONF'
DataDir: data
LogLevel: trace
Hostname: moxdirectbind.mox1.example
Postmaster:
	Account: moxdirectbind
	Mailbox: Postmaster
Listeners:
	public:
		IPs:
			- 172.28.1.90
		SMTP:
			Enabled: true
			Port: 2025
			NoRequireSTARTTLS: true
		Submission:
			Enabled: true
			Port: 2587
			NoRequireSTARTTLS: true
		IMAP:
			Enabled: true
			Port: 2143
			NoRequireSTARTTLS: true
		AccountHTTP:
			Enabled: true
			Port: 2080
		AdminHTTP:
			Enabled: true
			Port: 2080
MOXCONF

# Write domains.conf with the test account/domain.
cat > config/domains.conf <<'DOMAINSCONF'
Domains:
	mox1.example:
		LocalpartCatchallSeparator: +
Accounts:
	moxdirectbind:
		Domain: mox1.example
		Destinations:
			moxdirectbind@mox1.example: nil
DOMAINSCONF

# Ensure correct ownership so our non-root user can access everything.
chown -R "$MOX_UID:$MOX_UID" /tmp/mox

# Run mox serve as non-root user (without MOX_SOCKETS).
# The setpriv command drops to the target UID/GID.
setpriv --reuid="$MOX_UID" --regid="$MOX_UID" --clear-groups mox -checkconsistency serve &
MOX_PID=$!

# Wait for ctl socket to appear (indicates server is ready).
while true; do
	if test -e data/ctl; then
		# Set account password via ctl socket.
		echo -n directbindpass | setpriv --reuid="$MOX_UID" --regid="$MOX_UID" --clear-groups mox setaccountpassword moxdirectbind
		break
	fi
	sleep 0.1
done
wait $MOX_PID
