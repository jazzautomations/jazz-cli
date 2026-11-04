//go:build !windows

package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

// TestCardapioHashVerify drives hash_verify through tb.Exec("script", ...): a sidecar
// .sha256, a SHA256SUMS with one wrong line and one missing file, an explicit expected
// digest, a BSD-style md5 file, and the refusals (no checksum source, bad algo).
// A mismatch is a successful run (ok:true, match:false, confidence 1.0): the script
// worked; the disagreement is the answer.
func TestCardapioHashVerify(t *testing.T) {
	tb, root := scTestBox(t) // skips if python3 is absent

	sha := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	md5s := func(s string) string { h := md5.Sum([]byte(s)); return hex.EncodeToString(h[:]) }

	scWrite(t, root, "rel/app.tar.gz", "httpkit release 1.4.0\n")
	scWrite(t, root, "rel/app.tar.gz.sha256", sha("httpkit release 1.4.0\n")+"  app.tar.gz\n")
	scWrite(t, root, "rel/a.txt", "alpha\n")
	scWrite(t, root, "rel/b.txt", "beta\n")
	scWrite(t, root, "rel/SHA256SUMS",
		sha("alpha\n")+"  a.txt\n"+
			"0000000000000000000000000000000000000000000000000000000000000000  b.txt\n"+
			"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef  ghost.txt\n")
	scWrite(t, root, "rel/bsd.md5", "MD5 (a.txt) = "+md5s("alpha\n")+"\n")
	scWrite(t, root, "lonely/x.bin", "no checksum anywhere\n")

	// 1. sidecar <file>.sha256 found on its own
	v, out := scCall(t, tb, "hash_verify", map[string]any{"path": "rel/app.tar.gz"})
	d := scDataOf(v)
	if !scOK(v) || scConf(v) != 1.0 {
		t.Fatalf("sidecar: %v\n%s", v, out.Text)
	}
	if d["ok_count"] != 1.0 || d["fail_count"] != 0.0 || d["missing_count"] != 0.0 || d["truncated"] != false {
		t.Errorf("sidecar counts = ok %v fail %v missing %v truncated %v", d["ok_count"], d["fail_count"], d["missing_count"], d["truncated"])
	}
	if d["text"] != "1 of 1 files match (sha256)" {
		t.Errorf("sidecar text = %q", d["text"])
	}
	res, _ := d["results"].([]any)
	if len(res) != 1 {
		t.Fatalf("sidecar results = %v", d["results"])
	}
	r0 := res[0].(map[string]any)
	if r0["file"] != "rel/app.tar.gz" || r0["algo"] != "sha256" || r0["match"] != true || r0["reason"] != "match" ||
		r0["expected"] != sha("httpkit release 1.4.0\n") || r0["actual"] != r0["expected"] || r0["source"] != "rel/app.tar.gz.sha256" {
		t.Errorf("sidecar result = %v", r0)
	}

	// 2. a checksum file as path: every entry verified, sorted by file; one wrong, one missing
	v, out = scCall(t, tb, "hash_verify", map[string]any{"path": "rel/SHA256SUMS"})
	d = scDataOf(v)
	if !scOK(v) || out.IsErr || scConf(v) != 1.0 {
		t.Fatalf("SHA256SUMS: mismatch must still be ok:true with confidence 1.0: %v\n%s", v, out.Text)
	}
	if d["ok_count"] != 1.0 || d["fail_count"] != 1.0 || d["missing_count"] != 1.0 {
		t.Errorf("SHA256SUMS counts = ok %v fail %v missing %v", d["ok_count"], d["fail_count"], d["missing_count"])
	}
	if d["text"] != "MISMATCH: b.txt (sha256), 1 missing" {
		t.Errorf("SHA256SUMS text = %q", d["text"])
	}
	res, _ = d["results"].([]any)
	if len(res) != 3 {
		t.Fatalf("SHA256SUMS results = %v", d["results"])
	}
	want := []struct {
		file   string
		match  any
		reason string
	}{{"rel/a.txt", true, "match"}, {"rel/b.txt", false, "mismatch"}, {"rel/ghost.txt", nil, "file not found"}}
	for i, w := range want {
		r := res[i].(map[string]any)
		if r["file"] != w.file || r["match"] != w.match || r["reason"] != w.reason {
			t.Errorf("results[%d] = %v, want %+v", i, r, w)
		}
	}
	if r := res[1].(map[string]any); r["actual"] != sha("beta\n") {
		t.Errorf("mismatch must report the real digest: %v", r["actual"])
	}
	if r := res[2].(map[string]any); r["actual"] != nil {
		t.Errorf("missing file must have actual null: %v", r)
	}

	// 3. expected digest given directly (md5 by length), wrong on purpose
	v, _ = scCall(t, tb, "hash_verify", map[string]any{"path": "rel/a.txt", "expected": "ffffffffffffffffffffffffffffffff"})
	d = scDataOf(v)
	if !scOK(v) || d["fail_count"] != 1.0 || d["text"] != "MISMATCH: a.txt (md5)" {
		t.Errorf("expected(md5): %v", v)
	}
	r0 = scDataOf(v)["results"].([]any)[0].(map[string]any)
	if r0["algo"] != "md5" || r0["actual"] != md5s("alpha\n") || r0["source"] != "arg:expected" {
		t.Errorf("expected(md5) result = %v", r0)
	}
	// right digest with an algo prefix
	v, _ = scCall(t, tb, "hash_verify", map[string]any{"path": "rel/a.txt", "expected": "sha256:" + sha("alpha\n")})
	if d = scDataOf(v); !scOK(v) || d["ok_count"] != 1.0 || d["text"] != "1 of 1 files match (sha256)" {
		t.Errorf("expected(sha256 prefix): %v", v)
	}
	// algo disagreeing with the digest length is a refusal, not a silent guess
	v, out = scCall(t, tb, "hash_verify", map[string]any{"path": "rel/a.txt", "expected": "ffffffffffffffffffffffffffffffff", "algo": "sha256"})
	if scOK(v) || !out.IsErr {
		t.Errorf("md5-length digest with algo=sha256 must be refused: %v", v)
	}

	// 4. BSD format through sums=, the target is the file the caller named
	v, _ = scCall(t, tb, "hash_verify", map[string]any{"path": "rel/a.txt", "sums": "rel/bsd.md5"})
	d = scDataOf(v)
	if !scOK(v) || d["ok_count"] != 1.0 || d["text"] != "1 of 1 files match (md5)" {
		t.Errorf("bsd md5: %v", v)
	}
	r0 = d["results"].([]any)[0].(map[string]any)
	if r0["file"] != "rel/a.txt" || r0["algo"] != "md5" || r0["match"] != true || r0["source"] != "rel/bsd.md5" {
		t.Errorf("bsd md5 result = %v", r0)
	}

	// 5. a sums file that does not list the file: undecided (match null), lower confidence
	v, _ = scCall(t, tb, "hash_verify", map[string]any{"path": "rel/app.tar.gz", "sums": "rel/SHA256SUMS"})
	d = scDataOf(v)
	r0 = d["results"].([]any)[0].(map[string]any)
	if !scOK(v) || r0["match"] != nil || r0["reason"] != "not listed in rel/SHA256SUMS" || scConf(v) >= 1.0 {
		t.Errorf("not listed: %v", v)
	}
	if d["ok_count"] != 0.0 || d["fail_count"] != 0.0 {
		t.Errorf("not listed must count neither ok nor fail: %v", d)
	}

	// 6. refusals: no checksum source at all, unknown path, bad algo (schema), directory
	v, out = scCall(t, tb, "hash_verify", map[string]any{"path": "lonely/x.bin"})
	if scOK(v) || !out.IsErr {
		t.Errorf("no checksum source must be refused: %v", v)
	}
	v, out = scCall(t, tb, "hash_verify", map[string]any{"path": "rel/nope.txt"})
	if scOK(v) || !out.IsErr {
		t.Errorf("missing path must be refused: %v", v)
	}
	_, out = scCall(t, tb, "hash_verify", map[string]any{"path": "rel/a.txt", "algo": "crc32"})
	if !out.IsErr {
		t.Errorf("algo=crc32 must fail schema validation: %s", out.Text)
	}
	v, out = scCall(t, tb, "hash_verify", map[string]any{"path": "rel"})
	if scOK(v) || !out.IsErr {
		t.Errorf("directory must be refused: %v", v)
	}
	_, out = scCall(t, tb, "hash_verify", map[string]any{})
	if !out.IsErr {
		t.Errorf("path is required: %s", out.Text)
	}

	// 7. bare digest sidecar (sha1 by length) resolves to the file it sits beside
	sha1sum := sha1.Sum([]byte("beta\n"))
	scWrite(t, root, "rel/b.txt.sha1", fmt.Sprintf("%x\n", sha1sum[:]))
	v, _ = scCall(t, tb, "hash_verify", map[string]any{"path": "rel/b.txt", "algo": "sha1"})
	d = scDataOf(v)
	if !scOK(v) || d["ok_count"] != 1.0 || d["fail_count"] != 0.0 || d["text"] != "1 of 1 files match (sha1)" {
		t.Errorf("bare sha1 sidecar (algo=sha1 must ignore the SHA256SUMS line): %v", v)
	}
	// with algo=auto both sources apply: the sha1 sidecar matches, the SHA256SUMS line does not
	v, _ = scCall(t, tb, "hash_verify", map[string]any{"path": "rel/b.txt"})
	d = scDataOf(v)
	if !scOK(v) || d["ok_count"] != 1.0 || d["fail_count"] != 1.0 || d["text"] != "MISMATCH: b.txt (sha1/sha256)" {
		t.Errorf("two sources: %v", v)
	}
}
