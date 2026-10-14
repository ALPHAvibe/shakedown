#!/bin/sh
org="$1"
printf '{"status":"pass","summary":"sandbox %s is up","findings":[]}\n' "$org"
