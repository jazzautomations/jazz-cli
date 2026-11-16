package main

import (
	"encoding/json"
	"sort"
	"strings"
)

// Compaction hardening (ported from Hermes Agent's context_compressor):
// a deterministic pass before the model summary and a deterministic part
// after it, so what matters never depends on a weak model summarizing well.

const (
	chToolKeep   = 1200 // chars of an old tool result kept for the summarizer
	chUserItem   = 600  // chars per verbatim user message
	chUserTotal  = 3500 // chars of verbatim user messages in all
	chFilesTotal = 40   // touched paths listed
)

// chPrune returns a copy of head for the summarizer: old tool results cut to
// head+tail, reasoning dropped, images replaced by a marker. Calls and
// message order stay, so histories remain valid for every provider.
func chPrune(head []Message) []Message {
	out := make([]Message, len(head))
	for i, m := range head {
		if m.Role == "tool" && len(m.Content) > chToolKeep {
			m.Content = truncateHeadTail(m.Content, chToolKeep)
		}
		m.Reasoning = ""
		if len(m.Images) > 0 {
			m.Images = nil
			m.Content += "\n[image omitted]"
		}
		out[i] = m
	}
	return out
}

// chUserVerbatim: the user's own messages, newest kept when over budget.
func chUserVerbatim(head []Message) []string {
	var all []string
	for _, m := range head {
		if m.Role != "user" {
			continue
		}
		c := strings.TrimSpace(acStripReminders(m.Content))
		if c == "" || strings.HasPrefix(c, "<summary>") {
			continue
		}
		all = append(all, acClip(c, chUserItem))
	}
	total, start := 0, len(all)
	for start > 0 && total+len(all[start-1]) <= chUserTotal {
		total += len(all[start-1])
		start--
	}
	if start == len(all) && len(all) > 0 { // one huge message: keep it clipped
		start = len(all) - 1
	}
	return all[start:]
}

// chTouchedFiles: paths the history wrote or edited, in order of first use.
func chTouchedFiles(head []Message) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range head {
		for _, tc := range m.ToolCalls {
			if tc.Name != "write" && tc.Name != "edit" {
				continue
			}
			var a map[string]any
			if json.Unmarshal([]byte(tc.Args), &a) != nil {
				continue
			}
			p, _ := a["path"].(string)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) > chFilesTotal {
		out = out[len(out)-chFilesTotal:]
	}
	return out
}

// chAnchors is the deterministic block appended to any summary (digest:
// the summary is already acDigestHistory, which lists the requests).
func chAnchors(head []Message, digest bool) string {
	var b strings.Builder
	if us := chUserVerbatim(head); len(us) > 0 && !digest {
		b.WriteString("\n\n## User messages (verbatim, oldest first)\n")
		for _, u := range us {
			b.WriteString("> " + strings.ReplaceAll(u, "\n", "\n> ") + "\n")
		}
	}
	if fs := chTouchedFiles(head); len(fs) > 0 {
		sort.Strings(fs)
		b.WriteString("\n## Files written or edited before the cut\n" + strings.Join(fs, ", ") + "\n")
	}
	return b.String()
}

const chAfterNote = "<system-reminder>The earlier part of this conversation was compacted into the summary above. Your tools remain fully available: continue the work by calling them, from where it left off — do not just describe what you would do.</system-reminder>"
