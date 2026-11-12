package upstream

import (
	"bytes"
	"context"
	"crypto/sha3"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"time"
)

func powConfig(ua string, scripts []string, build string) []any {
	script := defaultPowScript
	if len(scripts) > 0 {
		script = scripts[rand.IntN(len(scripts))]
	}
	now := time.Now()
	east := time.FixedZone("EST", -5*3600)
	return []any{3000, now.In(east).Format("Mon Jan 02 2006 15:04:05") + " GMT-0500 (Eastern Standard Time)", 4294705152, 1, ua, script, build, "en-US", "en-US,es-US,en,es", rand.Float64(), "webdriver−false", "location", "performance", rand.Float64() * 10000, uuid(), "", 8, float64(now.UnixMilli()) - 10000, 0, 0, 0, 0, 0, 0, 0}
}
func encodeConfig(config []any) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(config) != nil {
		return "", ErrChallenge
	}
	return base64.StdEncoding.EncodeToString(bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))), nil
}
func requirementsToken(ua string, scripts []string, build string) (string, error) {
	encoded, err := encodeConfig(powConfig(ua, scripts, build))
	return "gAAAAAC" + encoded, err
}
func buildProof(ctx context.Context, seed, difficulty, ua string, scripts []string, build string) (string, error) {
	if len(seed) > 4096 || len(difficulty) < 2 || len(difficulty) > 128 || len(difficulty)%2 != 0 {
		return "", ErrChallenge
	}
	target, err := hex.DecodeString(difficulty)
	if err != nil {
		return "", ErrChallenge
	}
	config := powConfig(ua, scripts, build)
	for i := 0; i < 500000; i++ {
		if i%64 == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		config[3] = i
		config[9] = i >> 1
		encoded, err := encodeConfig(config)
		if err != nil {
			return "", err
		}
		hash := sha3.Sum512([]byte(seed + encoded))
		if bytes.Compare(hash[:len(target)], target) <= 0 {
			return "gAAAAAB" + encoded, nil
		}
	}
	return "", ErrChallenge
}
