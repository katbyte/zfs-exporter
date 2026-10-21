#!/bin/sh
# Rewrites what capture.sh printed so it can be shown to others, or kept as a test: every serial number and world wide name is swapped for a made-up
# one, the same one each time it appears. Models, paths, partition UUIDs and slots stay as they were.
#
#   .scripts/anonymize.sh < capture.txt > lib/host/hosttest/testdata/data.txt
#
# A world wide name keeps its first seven digits, which say who made the disk and not which one it is.
set -eu

exec awk -F '\t' '
BEGIN {
	OFS = "\t"
	for (i = 32; i < 127; i++) code[sprintf("%c", i)] = i
}

# fake is the made-up serial number that stands for a real one: as long, so what pads it stays as it was.
function fake(real) {
	if (!(real in serials)) serials[real] = sprintf("Z%0" (length(real) - 1) "d", ++made)

	return serials[real]
}

# number is what a run of hex digits says: not every awk has strtonum.
function number(hex,    i, n) {
	n = 0
	for (i = 1; i <= length(hex); i++) n = n * 16 + index("0123456789abcdef", substr(tolower(hex), i, 1)) - 1

	return n
}

# page is a page holding a serial number, in hex, with the number swapped for a made-up one: the four bytes that say what the page is stay, and so
# does the padding around the number.
function page(hex,    out, i, byte, first, last, real, swapped) {
	first = 0
	for (i = 9; i < length(hex); i += 2) {
		byte = substr(hex, i, 2)
		if (byte != "20" && byte != "00") {
			if (!first) first = i
			last = i
		}
	}
	if (!first) return hex

	for (i = first; i <= last; i += 2) real = real sprintf("%c", number(substr(hex, i, 2)))
	swapped = fake(real)

	out = substr(hex, 1, first - 1)
	for (i = 1; i <= length(swapped); i++) out = out sprintf("%02x", code[substr(swapped, i, 1)])

	return out substr(hex, last + 2)
}

# listed is the page of names of a disk, in hex, with each name its maker made up for it as text swapped as a serial number is, each world wide name
# swapped as one is, and the four bytes before each name as they were.
function listed(hex,    out, at, one) {
	out = substr(hex, 1, 8)
	for (at = 9; at + 7 <= length(hex); at += length(one)) {
		one = substr(hex, at, 8 + 2 * number(substr(hex, at + 6, 2)))
		if (number(substr(one, 2, 1)) == 2 && number(substr(one, 3, 2)) % 64 == 0) one = page(one)
		if (number(substr(one, 4, 1)) == 3) one = substr(one, 1, 8) name(substr(one, 9))
		out = out one
	}

	return out substr(hex, at)
}

# name is the made-up world wide name that stands for a real one.
function name(real) {
	if (!(real in names)) names[real] = substr(real, 1, 7) sprintf("%0" (length(real) - 7) "x", ++named)

	return names[real]
}

/^#/ { next }

{
	file = $2
	sub(/.*\//, "", file)
}

$1 == "x" && file == "vpd_pg80" { $3 = page($3); print; next }
$1 == "x" && file == "vpd_pg83" { $3 = listed($3); print; next }

$1 == "f" && file == "serial" {
	real = $3
	gsub(/^[ \t]+|[ \t]+$/, "", real)
	if (real != "") sub(real, fake(real), $3)
	print
	next
}

$1 == "f" && file == "wwid" && $3 ~ /^(naa|eui)\./ { $3 = substr($3, 1, 4) name(substr($3, 5)); print; next }

{ print }
'
