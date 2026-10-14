#!/bin/sh
ref="$1"
printf '{"status":"pass","summary":"migrated %s","findings":[]}\n' "$ref"
