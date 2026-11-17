#!/bin/sh
# gh stub of the drift fixture: pr view <url> --json state
case "$3" in
 */pull/88) echo '{"state": "MERGED"}';;
 */pull/89) echo '{"state": "OPEN"}';;
 *) echo 'gh: no pull request found' >&2; exit 1;;
esac
