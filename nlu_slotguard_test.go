package main

import "testing"

// Slot guards: a typed slot must never swallow a leftover word of the wrong
// kind — "regular/normal" is not a limit, "megabytes" is not a directory,
// "last week" is a time window. These were the dominant v14 failure
// signature (path not found: were/last/megabytes, limit must be a number).
func TestNLUSlotGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	// number-kind slots: digits only, never a leftover name
	for _, text := range []string{
		"show all regular/normal the files in the current folder whose size is bigger than 0 bytes",
		"search files whose size is 10 bytes in regular format",
		"find files bigger than 5 megabytes and list the first 20",
	} {
		in := m.Parse(text, w)
		if v := in.Slots["limit"]; v != "" && !nuIsNumber(v) {
			t.Errorf("%q limit=%q — not a number (%v)", text, v, in.Slots)
		}
	}
	// units and time words after "in" are never a directory
	for text, bad := range map[string]string{
		"search files in megabytes":                 "megabytes",
		"find files in kilobytes":                   "kilobytes",
		"list files in bytes":                       "bytes",
		"find files modified in the last week":      "last",
		"show files from in the past month":         "past",
		"files modified in the previous 2 days":     "previous",
		"find files inside $DIR":                    "$DIR",
		"find files in the future releases":         "future",
	} {
		in := m.Parse(text, w)
		if in.Slots["dir"] == bad {
			t.Errorf("%q dir=%q — %q is not a directory (%v)", text, bad, bad, in.Slots)
		}
	}
	// a real cued dir still resolves
	in := m.Parse("acha arquivos na pasta downloads", w)
	if in.Slots["dir"] != "downloads" {
		t.Errorf("cued dir lost: %v", in.Slots)
	}
	// a numeric limit still fills from a real number
	in = m.Parse("find files limit 10", w)
	if v := in.Slots["limit"]; v != "" && !nuIsNumber(v) {
		t.Errorf("limit=%q not numeric", v)
	}
	// owner/group names: quoted names, conjunctions, paths and the
	// "no owner" EN-negation that the PT "no" preposition must not eat
	ownerw := map[string]map[string]string{
		"files owned by user 'takuya'":                    {"owner": "takuya"},
		"files that belong to the group adm":              {"group": "adm"},
		"files which belong to id":                        {"owner": "id"},
		"files with no owner or group":                    {"orphan": "true"},
		"files not owned by 'daniel' in the logs dir":     {"not_owner": "daniel"},
	}
	for text, wants := range ownerw {
		in := m.Parse(text, w)
		for k, v := range wants {
			if in.Slots[k] != v {
				t.Errorf("%q slots %s=%q want %q (%v)", text, k, in.Slots[k], v, in.Slots)
			}
		}
		if in.Slots["dir"] == "owner" || in.Slots["owner"] == "or" || in.Slots["group"] == "or" {
			t.Errorf("%q poisoned: %v", text, in.Slots)
		}
	}
}
