package isureal

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const imageURLLifetime = 24 * time.Hour
const imageSignatureIterations = 1000

// 発行する URL の exp をこの秒数の倍数に切り上げる。同じ画像の URL は刻みの間ずっと同じ exp になり、
// 署名のキャッシュが当たる。有効期限は最大でこの分だけ 24 時間より長くなる（運営の回答で、ベンチマーカーが許容する範囲なら可）
const imageURLExpStep = 3600

// 署名は (鍵, 画像 ID, exp) だけで決まり副作用もないので、計算結果を使い回す。上限を超えたら捨てて作り直す
const imageSignatureCacheLimit = 500000

var imageSignatureCache = struct {
	sync.RWMutex
	m map[string][]byte
}{m: make(map[string][]byte)}

func imageSigningKey(secretKeyHash string) ([]byte, error) {
	key, err := hex.DecodeString(secretKeyHash)
	if err != nil || len(key) != sha256.Size {
		return nil, fmt.Errorf("invalid image signing key")
	}
	return key, nil
}

func imageSignature(key []byte, imageID string, exp int64) []byte {
	cacheKey := string(key) + "\x00" + imageID + "\x00" + strconv.FormatInt(exp, 10)
	imageSignatureCache.RLock()
	sig, ok := imageSignatureCache.m[cacheKey]
	imageSignatureCache.RUnlock()
	if ok {
		return sig
	}
	sig = computeImageSignature(key, imageID, exp)
	imageSignatureCache.Lock()
	if len(imageSignatureCache.m) >= imageSignatureCacheLimit {
		imageSignatureCache.m = make(map[string][]byte)
	}
	imageSignatureCache.m[cacheKey] = sig
	imageSignatureCache.Unlock()
	return sig
}

func resetImageSignatureCache() {
	imageSignatureCache.Lock()
	imageSignatureCache.m = make(map[string][]byte)
	imageSignatureCache.Unlock()
}

// HMAC-SHA256 を imageSignatureIterations 回繰り返す。hmac を作り直さずに Reset で使い回す（出力は毎回作り直す場合と同じ）
func computeImageSignature(key []byte, imageID string, exp int64) []byte {
	mac := hmac.New(sha256.New, key)
	value := []byte(strconv.FormatInt(exp, 10) + ":" + imageID)
	for range imageSignatureIterations {
		mac.Reset()
		mac.Write(value)
		// Write で value は読み終えているので、同じ領域に結果を書いてよい
		value = mac.Sum(value[:0])
	}
	return value
}

func imageURLExp(now time.Time) int64 {
	exp := now.Add(imageURLLifetime).Unix()
	return (exp + imageURLExpStep - 1) / imageURLExpStep * imageURLExpStep
}

func signedImageURL(user User, imageID string) string {
	exp := imageURLExp(time.Now())
	query := url.Values{
		"exp": {strconv.FormatInt(exp, 10)},
		"sig": {base64.RawURLEncoding.EncodeToString(imageSignature(user.ImageKey, imageID, exp))},
	}
	return "/api/images/" + url.PathEscape(imageID) + "?" + query.Encode()
}
