package main

// quota429.go: telling a "free quota used up" 429 apart from a plain "slow down"
// 429, and reading when the quota comes back from the body. Owner: providers.
//
// A quota 429 (kilo's PROMOTION_MODEL_LIMIT_REACHED, "quota exceeded", "free
// usage limit reached"…) will not clear in seconds, so retrying the same
// provider is pointless: pvMarkQuota flags the error Quota+Daily, which makes the
// agent move to the next candidate and the limiter cool the whole provider
// (ratelimit.go classifyLocked) until the parsed reset, or pvQuotaDefault.
// Because it is not Rate, the judge does not count it as a 429 (judge.go).

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// pvQuotaDefault is how long a provider cools after a quota 429 that says nothing about its reset.
const pvQuotaDefault = 15 * time.Minute

var (
	// Deliberately narrow: Groq/OpenRouter per-minute 429s also say "limit reached" and must stay plain rate errors.
	pvQuotaRE = regexp.MustCompile(`(?-i:[A-Z]+_LIMIT_REACHED)|(?i)quota[ _-]?(exceeded|exhausted)|exceeded your (current )?(quota|free)|free (usage|tier|allowance|promotion) (limit|quota|has|is)|promotion\w* (limit|quota)|out of (free )?(credits|quota)|resource_exhausted`)
	// "resets in 5 minutes", "try again in 2h30m", "retry after 90 seconds", "available again in 3 hours"
	pvResetInRE = regexp.MustCompile(`(?i)(?:reset[s]?|try again|retry|available again|come[s]? back)[^0-9]{0,24}?(?:in|after)?\s*(\d+(?:\.\d+)?)\s*(ms|milliseconds?|s|secs?|seconds?|m|mins?|minutes?|h|hrs?|hours?|d|days?)\b`)
	// "try again in 1h2m3s" (Go-style compound durations)
	pvResetCompRE = regexp.MustCompile(`(?i)(?:in|after)\s+((?:\d+(?:\.\d+)?[hms]\s*)+)`)
	// "resets_at": "2026-10-27T00:00:00Z" / "reset_at" / "resetsAt" / "resets at 2026-…"
	pvResetAtRE = regexp.MustCompile(`(?i)reset[s_ ]*(?:at|time)?"?\s*[:=]?\s*"?(\d{4}-\d\d-\d\dT[\d:.]+(?:Z|[+-]\d\d:?\d\d)?)`)
)

// pvIsQuotaBody reports whether a 429/402/403 body says the free quota is used up.
func pvIsQuotaBody(status int, body string) bool {
	if status != 429 && status != 402 && status != 403 {
		return false
	}
	return pvQuotaRE.MatchString(body)
}

// pvBodyReset reads how long until the quota comes back from the message text.
// Returns 0 when the body does not say.
func pvBodyReset(body string, now time.Time) time.Duration {
	if m := pvResetAtRE.FindStringSubmatch(body); m != nil {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04:05Z0700"} {
			if t, err := time.Parse(layout, m[1]); err == nil {
				if d := t.Sub(now); d > 0 {
					return d
				}
			}
		}
	}
	if m := pvResetCompRE.FindStringSubmatch(body); m != nil {
		if d, err := time.ParseDuration(strings.ReplaceAll(strings.ToLower(m[1]), " ", "")); err == nil && d > 0 {
			return d
		}
	}
	if m := pvResetInRE.FindStringSubmatch(body); m != nil {
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil || n <= 0 {
			return 0
		}
		var unit time.Duration
		switch u := strings.ToLower(m[2]); {
		case strings.HasPrefix(u, "ms"), strings.HasPrefix(u, "milli"):
			unit = time.Millisecond
		case strings.HasPrefix(u, "s"):
			unit = time.Second
		case strings.HasPrefix(u, "m"):
			unit = time.Minute
		case strings.HasPrefix(u, "h"):
			unit = time.Hour
		case strings.HasPrefix(u, "d"):
			unit = 24 * time.Hour
		}
		return time.Duration(n * float64(unit))
	}
	return 0
}

// pvMarkQuota flags ae as a quota error when its body says so, and fills
// RetryAfter from the body when the headers gave nothing (or pvQuotaDefault).
// Quota errors are also Daily so the agent skips to the next candidate.
func pvMarkQuota(ae *APIError, now time.Time) {
	if ae == nil || ae.Daily || !pvIsQuotaBody(ae.Status, ae.Body) { // Daily already handled per day/month (pvIsDaily)
		return
	}
	ae.Quota, ae.Daily = true, true
	if ae.RetryAfter <= 0 {
		ae.RetryAfter = pvBodyReset(ae.Body, now)
	}
	if ae.RetryAfter <= 0 {
		ae.RetryAfter = pvQuotaDefault
	}
}
