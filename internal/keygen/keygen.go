// Package keygen produces keys via the same AWG image the server runs, so
// formats always match — the host only needs Docker. No crypto dependencies.
package keygen

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	mrand "math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Docker-based key generation, mirroring awg_mesh/keygen.py. --entrypoint is
// load-bearing: images built with their own entrypoint (e.g. the Amnezia
// app's container) would run it and hang instead of generating keys.

func dockerRun(input string, args ...string) (string, string, int) {
	cmd := exec.Command("docker", args...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		code = -1
	}
	return stdout.String(), stderr.String(), code
}

// GenKeypair returns (private, public) from `awg` inside the image.
func GenKeypair(image string) (string, string, error) {
	privOut, privErr, code := dockerRun("", "run", "--rm", "--network", "none",
		"--entrypoint", "awg", image, "genkey")
	if code != 0 {
		return "", "", fmt.Errorf("awg genkey failed (image: %s): %s", image, strings.TrimSpace(privErr))
	}
	priv := strings.TrimSpace(privOut)
	pubOut, pubErr, code := dockerRun(priv, "run", "--rm", "--network", "none",
		"--entrypoint", "awg", "-i", image, "pubkey")
	if code != 0 {
		return "", "", fmt.Errorf("awg pubkey failed (image: %s): %s", image, strings.TrimSpace(pubErr))
	}
	return priv, strings.TrimSpace(pubOut), nil
}

// GenPSK returns a fresh preshared key.
func GenPSK(image string) (string, error) {
	out, errOut, code := dockerRun("", "run", "--rm", "--network", "none",
		"--entrypoint", "awg", image, "genpsk")
	if code != 0 {
		return "", fmt.Errorf("awg genpsk failed (image: %s): %s", image, strings.TrimSpace(errOut))
	}
	return strings.TrimSpace(out), nil
}

// PubFromPriv derives a public key from an existing private key (used by adopt).
func PubFromPriv(image, priv string) (string, error) {
	pubOut, pubErr, code := dockerRun(strings.TrimSpace(priv), "run", "--rm",
		"--network", "none", "--entrypoint", "awg", "-i", image, "pubkey")
	if code != 0 {
		return "", fmt.Errorf("awg pubkey failed (image: %s): %s", image, strings.TrimSpace(pubErr))
	}
	return strings.TrimSpace(pubOut), nil
}

// ProbeScript is the I1-I5 (CPS) support probe: bring up a throwaway
// interface whose config contains an I1 line. Success means the parser
// accepts it. Never fatal — init records the result in server.env.
const ProbeScript = `
command -v awg-quick >/dev/null 2>&1 || { echo NO_AWGQUICK; exit 0; }
d=$(mktemp -d)
priv=$(awg genkey)
printf "[Interface]\nPrivateKey = %s\nAddress = 10.213.0.1/30\nListenPort = 23199\nI1 = 30-50\n[Peer]\nPublicKey = %s\nAllowedIPs = 10.213.0.2/32\n" "$priv" "$priv" > "$d/p.conf"
if awg-quick up "$d/p.conf" >/dev/null 2>&1; then
  awg-quick down "$d/p.conf" >/dev/null 2>&1
  echo SUPPORTED
else
  echo UNSUPPORTED
fi
rm -rf "$d"
`

// ProbeISupport returns "SUPPORTED" | "UNSUPPORTED" | "NO_AWGQUICK".
func ProbeISupport(image string) string {
	out, _, code := dockerRun("", "run", "--rm", "--network", "none",
		"--device", "/dev/net/tun:/dev/net/tun",
		"--entrypoint", "sh", image, "-c", ProbeScript)
	if code != 0 {
		return "UNSUPPORTED"
	}
	switch v := strings.TrimSpace(out); v {
	case "SUPPORTED", "UNSUPPORTED", "NO_AWGQUICK":
		return v
	}
	return "UNSUPPORTED"
}

// obfSource abstracts the RNG so tests and the parity harness can pin
// obfuscation output via AWG_MESH_SEED (AWG_MESH_SEED in env → deterministic).
type obfSource interface {
	Intn(n int) int
	Bytes(n int) []byte
}

type cryptoObf struct{}

func (cryptoObf) Intn(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(fmt.Sprintf("keygen: crypto/rand: %v", err))
	}
	return int(v.Int64())
}

func (cryptoObf) Bytes(n int) []byte {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic(fmt.Sprintf("keygen: crypto/rand: %v", err))
	}
	return b
}

type seededObf struct{ r *mrand.Rand }

func (s seededObf) Intn(n int) int { return s.r.Intn(n) }

func (s seededObf) Bytes(n int) []byte {
	b := make([]byte, n)
	s.r.Read(b)
	return b
}

func newObfSource() obfSource {
	if seed := os.Getenv("AWG_MESH_SEED"); seed != "" {
		if n, err := strconv.ParseInt(seed, 10, 64); err == nil {
			return seededObf{mrand.New(mrand.NewSource(n))}
		}
	}
	return cryptoObf{}
}

// GenObfuscation returns the randomized per-deployment obfuscation profile
// (AmneziaWG junk-packet handshake parameters) as KEY=VALUE lines, same
// ranges as the original implementation. Deterministic when AWG_MESH_SEED
// is set; production draws from crypto/rand.
func GenObfuscation() []string {
	return genObfuscation(newObfSource())
}

func genObfuscation(r obfSource) []string {
	jmin := 10 + r.Intn(11) // randint(10, 20) inclusive
	lines := []string{
		fmt.Sprintf("OBF_JC=%d", 3+r.Intn(5)),
		fmt.Sprintf("OBF_JMIN=%d", jmin),
		fmt.Sprintf("OBF_JMAX=%d", jmin+30+r.Intn(21)), // randint(jmin+30, jmin+50)
	}
	for i, v := range sample(r, 30, 220, 4) { // sample(range(30, 220), 4)
		lines = append(lines, fmt.Sprintf("OBF_S%d=%d", i+1, v))
	}
	for i, v := range sample(r, 1, 1<<32, 4) { // sample(range(1, 2**32), 4)
		lines = append(lines, fmt.Sprintf("OBF_H%d=%d", i+1, v))
	}
	lines = append(lines,
		"OBF_HEADERPROTECTIONKEY="+base64.StdEncoding.EncodeToString(r.Bytes(32)),
		"OBF_CONTENTPADDINGADDITION=2-10",
		"OBF_REKEYAFTERTIME=120-180",
		"OBF_REKEYTIMEOUT=5-10",
		"OBF_REJECTAFTERTIME=540-600",
		"OBF_KEEPALIVETIMEOUT=8-12",
		"OBF_MAXHANDSHAKEATTEMPTS=80-100",
		"OBF_RANDOMTRAILERS=on",
		"OBF_DISABLECOOKIES=on",
	)
	return lines
}

// sample picks n distinct values from [lo, hi), like Python's
// random.sample(range(lo, hi), n).
func sample(r obfSource, lo, hi, n int) []int64 {
	used := map[int64]bool{}
	out := make([]int64, 0, n)
	for len(out) < n {
		v := int64(lo + r.Intn(hi-lo))
		if !used[v] {
			used[v] = true
			out = append(out, v)
		}
	}
	return out
}
