#!/bin/sh
# Stands in for filees-ssh-auth: same arguments, same protocol on descriptor 3.
# FAKE_MODE is baked in by the test runner (the module starts the helper with an
# empty environment, exactly as it does the real one).
mode=@MODE@
[ "$1" = "-s" ] && [ "$3" = "_filees-tunnel" ] && [ "$4" = "filees-tunnel" ] || { printf 'reject\n' >&3; exit 64; }
id -un > @WHO@ 2>/dev/null
case "$2" in
challenge)
	case "$mode" in
	nochallenge) printf 'reject\n' >&3 ;;
	hang) sleep 60 ;;
	# %s keeps the two characters backslash-n literal, as the real helper
	# sends them; BSD Authentication (and the module) decode that escape.
	*) printf '%s\n' 'value challenge FileES authentication nonce: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA:\n' 'reject challenge' >&3 ;;
	esac ;;
response)
	fields=$(tr '\0' '\n' <&3)
	challenge=$(printf '%s\n' "$fields" | sed -n '1p')
	answer=$(printf '%s\n' "$fields" | sed -n '3p')
	printf '%s\n' "$fields" > @SEEN@
	case "$mode" in
	chatty) printf 'authorize\nextra\n' >&3 ;;
	*) if [ "$challenge" = "FileES authentication nonce: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA:" ] && [ "$answer" = "good-otp" ]; then printf 'authorize\n' >&3; else printf 'reject\n' >&3; fi ;;
	esac ;;
*) printf 'reject\n' >&3; exit 64 ;;
esac
