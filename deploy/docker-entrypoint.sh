#!/bin/sh
set -eu

if [ "$(id -u)" = "0" ]; then
  chown -R app:app "${DATA_DIR:-/data}"
  exec su-exec app:app /usr/local/bin/115-direct "$@"
fi

exec /usr/local/bin/115-direct "$@"
