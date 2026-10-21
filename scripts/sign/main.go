// Command sign creates the ed25519 key pair for `jazz update` and signs
// release files with it (stdlib only).
//
//	go run ./scripts/sign -gen ~/.config/jazz-release.key   # prints the public key
//	go run ./scripts/sign -pub ~/.config/jazz-release.key   # prints it again (base64)
//	go run ./scripts/sign -minisign-pub ~/.config/jazz-release.key   # minisign -P form
//	go run ./scripts/sign -key ~/.config/jazz-release.key dist/release/manifest.txt
//
// Signing writes <file>.sig (base64 ed25519 over the file, what jazz update
// checks) and <file>.minisig (minisign legacy "Ed" format, so scripts/install.sh
// can verify with `minisign -V -P <key>`). Build the release with the public
// key embedded (scripts/release.sh does it) so jazz refuses unsigned updates.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	gen := flag.String("gen", "", "create a new private key file (0600) and print the public key")
	key := flag.String("key", "", "private key file used to sign")
	pub := flag.String("pub", "", "print the base64 public key of this private key file")
	mpub := flag.String("minisign-pub", "", "print the minisign public key (for minisign -P) of this private key file")
	flag.Parse()
	switch {
	case *gen != "":
		if _, err := os.Stat(*gen); err == nil {
			die("%s já existe: não sobrescrevo uma chave", *gen)
		}
		pk, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			die("%v", err)
		}
		if err := os.WriteFile(*gen, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
			die("%v", err)
		}
		fmt.Println(base64.StdEncoding.EncodeToString(pk))
	case *pub != "":
		priv := load(*pub)
		fmt.Println(base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
	case *mpub != "":
		priv := load(*mpub)
		pk := priv.Public().(ed25519.PublicKey)
		fmt.Println(base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyID(pk)...), pk...)))
	case *key != "" && flag.NArg() >= 1:
		priv := load(*key)
		for _, f := range flag.Args() {
			data, err := os.ReadFile(f)
			if err != nil {
				die("%v", err)
			}
			sig := ed25519.Sign(priv, data)
			if err := os.WriteFile(f+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
				die("%v", err)
			}
			if err := os.WriteFile(f+".minisig", minisig(priv, filepath.Base(f), sig), 0o644); err != nil {
				die("%v", err)
			}
		}
	default:
		die("uso: sign -gen <chave> | -pub <chave> | -minisign-pub <chave> | -key <chave> <arquivo>…")
	}
}

func load(p string) ed25519.PrivateKey {
	b, err := os.ReadFile(p)
	if err != nil {
		die("%v", err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		die("chave privada inválida em %s", p)
	}
	return ed25519.NewKeyFromSeed(seed)
}

// keyID is the 8-byte minisign key id (first bytes of sha256 of the key).
func keyID(pk ed25519.PublicKey) []byte {
	h := sha256.Sum256(pk)
	return h[:8]
}

// minisig renders a minisign legacy signature: the "Ed" algorithm signs the
// file itself; the global signature covers sig || trusted comment.
func minisig(priv ed25519.PrivateKey, name string, sig []byte) []byte {
	pk := priv.Public().(ed25519.PublicKey)
	trusted := fmt.Sprintf("timestamp:%d\tfile:%s", time.Now().Unix(), name)
	global := ed25519.Sign(priv, append(append([]byte(nil), sig...), trusted...))
	var b strings.Builder
	b.WriteString("untrusted comment: jazz-cli release signature\n")
	b.WriteString(base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyID(pk)...), sig...)) + "\n")
	b.WriteString("trusted comment: " + trusted + "\n")
	b.WriteString(base64.StdEncoding.EncodeToString(global) + "\n")
	return []byte(b.String())
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "sign: "+f+"\n", a...)
	os.Exit(1)
}
