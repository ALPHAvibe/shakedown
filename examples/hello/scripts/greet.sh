#!/bin/sh
name="$1"
printf '{"status":"pass","summary":"hello %s","findings":[]}\n' "$name"
