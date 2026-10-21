package main

import (
	"fmt"
	"strings"
	"testing"
)

// The tail buffer is trimmed lazily (up to 2*tailCap), so upTo must still
// return at most headCap+tailCap bytes no matter how the output was chunked.
// On linux/386 the pipe hands over 64 KiB chunks and the kept output went
// past the limit.
func TestOutBufKeepsAtMostCaps(t *testing.T) {
	for _, chunk := range []int{1, 512, 4096, 65536, 100000} {
		ob := &outBuf{headCap: 16 << 10, tailCap: 48 << 10}
		total := 1000000
		data := []byte(strings.Repeat("a", total-3) + "END")
		for i := 0; i < len(data); i += chunk {
			j := i + chunk
			if j > len(data) {
				j = len(data)
			}
			ob.write(data[i:j])
		}
		out, dropped := ob.upTo(ob.total)
		body := strings.Replace(out, fmt.Sprintf("\n[… %d bytes omitted …]\n", dropped), "", 1)
		if len(body) != ob.headCap+ob.tailCap {
			t.Errorf("chunk %d: kept %d bytes, want %d", chunk, len(body), ob.headCap+ob.tailCap)
		}
		if dropped != total-ob.headCap-ob.tailCap {
			t.Errorf("chunk %d: dropped %d, want %d", chunk, dropped, total-ob.headCap-ob.tailCap)
		}
		if !strings.HasSuffix(out, "END") {
			t.Errorf("chunk %d: tail lost the end", chunk)
		}
	}
}
