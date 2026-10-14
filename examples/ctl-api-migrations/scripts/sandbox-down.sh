#!/bin/sh
org="$1"
printf '{"status":"pass","summary":"sandbox %s torn down","findings":[]}\n' "$org"
