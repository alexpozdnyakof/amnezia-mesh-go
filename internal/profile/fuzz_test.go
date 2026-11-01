package profile

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/ojson"
)

// FuzzCodecRoundtrip: encode→decode must be lossless for any JSON object
// the fuzzer builds, and decode of arbitrary vpn:// payloads must fail
// cleanly (error, never a panic or hang). Seeds double as plain unit-test
// cases under `go test`.
func FuzzCodecRoundtrip(f *testing.F) {
	marshal := func(o *ojson.Obj) string {
		var b bytes.Buffer
		o.Marshal(&b)
		return b.String()
	}
	seed := ojson.New()
	seed.Set("description", "laptop — exit, «quoted» \"json\" 😀")
	seed.Set("hostName", "192.0.2.10")
	seed.Set("empty", "")
	c := ojson.New()
	c.Set("container", "amnezia-awg")
	awg := ojson.New()
	awg.Set("last_config", `{"config":"[Interface]\nPrivateKey = x\n","allowed_ips":["0.0.0.0/0"]}`)
	c.Set("awg", awg)
	seed.Set("containers", []any{c})
	url, err := EncodeProfile(seed)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(url)
	f.Add("vpn://AAAA")
	f.Add("not-a-vpn-url")
	f.Add("vpn://" + strings.Repeat("A", 256))

	f.Fuzz(func(t *testing.T, input string) {
		if decoded, err := DecodeProfile(input); err == nil {
			// Anything that decodes must re-encode and decode identically.
			reencoded, err := EncodeProfile(decoded)
			if err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			again, err := DecodeProfile(reencoded)
			if err != nil {
				t.Fatalf("re-decode: %v", err)
			}
			if marshal(again) != marshal(decoded) {
				t.Fatalf("roundtrip mismatch:\n%v\n%v", decoded, again)
			}
		}
		// A structurally valid object must survive encode→decode verbatim.
		if strings.HasPrefix(input, "{") {
			obj, err := ojson.ParseObj([]byte(input))
			if err != nil {
				return
			}
			url, err := EncodeProfile(obj)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			back, err := DecodeProfile(url)
			if err != nil {
				t.Fatalf("decode own output: %v", err)
			}
			if marshal(back) != marshal(obj) {
				t.Fatalf("lossy codec:\n%v\n%v", obj, back)
			}
		}
	})
}
