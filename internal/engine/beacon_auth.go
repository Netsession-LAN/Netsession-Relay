package engine

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// beaconDomain separates the beacon MAC from any other use of the same secret.
const beaconDomain = "nsrelay-beacon-v1\x00"

// SignBeacon appends the signature trailer to a frame from BuildBeacon: the send time and an HMAC-SHA256
// (truncated to 16 bytes) over the whole payload including the time, keyed with the shared secret. The version
// stays 2, so engines without signature support still read the beacon.
func SignBeacon(f, key []byte, now time.Time) []byte {
	p := f[pkt.EthHeaderLen:]
	p[beaconOffFlags] |= beaconFlagSigned
	var ts [beaconTimeLen]byte
	binary.BigEndian.PutUint64(ts[:], uint64(now.UnixNano()))
	f = append(f, ts[:]...)
	return append(f, beaconTag(key, f[pkt.EthHeaderLen:])...)
}

func beaconTag(key, signed []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(beaconDomain))
	m.Write(signed)
	return m.Sum(nil)[:beaconTagLen]
}

// BeaconAuth checks the signature of a beacon frame that ParseBeacon accepted. It returns "" when the beacon is
// signed with key and fresh, otherwise why not: "unsigned" (an engine without key, an old version, or a forged
// frame), "signature" (another key, or forged) or "clock" (older or newer than BeaconMaxSkew: a replay, or the
// clocks of the two relays differ).
func BeaconAuth(f, key []byte, now time.Time) string {
	p := f[pkt.EthHeaderLen:]
	end, ok := beaconTrailer(p)
	if !ok {
		return "unsigned"
	}
	if !hmac.Equal(beaconTag(key, p[:end+beaconTimeLen]), p[end+beaconTimeLen:end+beaconSigLen]) {
		return "signature"
	}
	sent := time.Unix(0, int64(binary.BigEndian.Uint64(p[end:end+beaconTimeLen])))
	if d := now.Sub(sent); d > BeaconMaxSkew || d < -BeaconMaxSkew {
		return "clock"
	}
	return ""
}

// beaconTrailer returns where the signature trailer of beacon payload p starts (after the port ranges); false if
// p carries no complete trailer.
func beaconTrailer(p []byte) (int, bool) {
	if len(p) < beaconOffRanges || p[beaconOffVersion] != beaconVersion || p[beaconOffFlags]&beaconFlagSigned == 0 {
		return 0, false
	}
	end := beaconOffRanges + int(binary.BigEndian.Uint16(p[beaconOffCount:beaconOffRanges]))*beaconRangeBytes
	return end, len(p) >= end+beaconSigLen
}

// LoadBeaconKey returns the shared secret for beacons and where it came from: the environment value
// (NSRELAY_BEACON_KEY) when set, else the content of file, which is created with a random key on first use
// (mode 0600, next to the snapshot in the engine's state directory). So beacons are signed without any
// configuration; a cloned VM carries the same file and is still recognised. Relays that are installed
// separately and must recognise each other get the same value through the environment.
func LoadBeaconKey(env, file string) (key []byte, source string, err error) {
	if env = strings.TrimSpace(env); env != "" {
		if len(env) < minBeaconKey {
			return nil, "", errors.New("NSRELAY_BEACON_KEY is shorter than 16 characters")
		}
		return []byte(env), "env", nil
	}
	if file == "" {
		return nil, "", errors.New("no state directory for the beacon key")
	}
	return keyFromFile(readOrCreateBeaconKey(file))
}

// readOrCreateBeaconKey reads the key from file, which is created with a random key first if it does not exist.
func readOrCreateBeaconKey(file string) ([]byte, error) {
	for range 2 {
		k, err := readBeaconKey(file, " (delete it to get a new one)")
		if !errors.Is(err, os.ErrNotExist) {
			return k, err
		}
		err = createBeaconKey(file)
		if errors.Is(err, os.ErrExist) {
			// O_EXCL: when two engines start at once, one creates the file and the other reads it
			time.Sleep(50 * time.Millisecond) // let the other one finish writing
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return readBeaconKey(file, "")
}

// minBeaconKey is the shortest accepted beacon key (characters).
const minBeaconKey = 16

func keyFromFile(k []byte, err error) ([]byte, string, error) {
	if err != nil {
		return nil, "", err
	}
	return k, "file", nil
}

// readBeaconKey reads the key from file; a key that is too short is an error, with hint appended to its text.
func readBeaconKey(file, hint string) ([]byte, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if k := strings.TrimSpace(string(b)); len(k) >= minBeaconKey {
		return []byte(k), nil
	}
	return nil, fmt.Errorf("%s: too short for a key%s", file, hint)
}

// createBeaconKey writes a new random key to file (mode 0600); os.ErrExist if the file exists already. A file that
// could not be written completely is removed.
func createBeaconKey(file string) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	fh, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := fh.WriteString(hex.EncodeToString(raw) + "\n")
	if cerr := fh.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(file)
	}
	return werr
}

// SetBeaconKey switches beacon signing and checking on (before the engine starts receiving).
func (g *Guard) SetBeaconKey(key []byte) { g.beaconKey = key }
