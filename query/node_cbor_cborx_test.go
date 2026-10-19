package query

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os/exec"
	"reflect"
	"testing"
)

// cbor-x is the writer of structures that rel's request reader exists for : its documents, read by cbor-x itself and
// by NewCBORNode, must give the same value. Skipped when bun or the TypeScript dependencies are missing.
func TestCBORNode_ReadsWhatCborXWrites(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed")
	}
	cmd := exec.Command(bun, "run", "testdata/cborx_records.ts")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("the cbor-x script does not run (bun install in typescript/ ?): %v %s", err, stderr.String())
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 1<<24)
	count := 0
	for sc.Scan() {
		var line struct{ Name, Hex, JSON string }
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		raw, err := hex.DecodeString(line.Hex)
		if err != nil {
			t.Fatal(err)
		}
		n, err := NewCBORNode(raw)
		if err != nil {
			t.Errorf("%s: %v", line.Name, err)
			continue
		}
		text, err := n.Raw()
		if err != nil {
			t.Errorf("%s: Raw: %v", line.Name, err)
			continue
		}
		var got, want any
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Errorf("%s: Raw is not JSON: %v", line.Name, err)
			continue
		}
		if err := json.Unmarshal([]byte(line.JSON), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: rel reads %.200s, cbor-x reads %.200s", line.Name, text, line.JSON)
		}
		count++
	}
	if count < 10 {
		t.Errorf("only %d documents compared", count)
	}
	t.Logf("%d documents, each read the same by cbor-x and rel", count)
}
