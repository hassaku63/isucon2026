package isureal

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"strconv"
	"testing"
	"time"
)

// 変更前の実装。ベンチマーカーはこの方法で署名を作るので、出力が一致しなければならない
func referenceImageSignature(key []byte, imageID string, exp int64) []byte {
	value := []byte(strconv.FormatInt(exp, 10) + ":" + imageID)
	for range imageSignatureIterations {
		mac := hmac.New(sha256.New, key)
		mac.Write(value)
		value = mac.Sum(nil)
	}
	return value
}

func TestImageSignatureMatchesReference(t *testing.T) {
	resetImageSignatureCache()
	for i := range 500 {
		key := make([]byte, sha256.Size)
		rand.Read(key)
		imageID := strconv.Itoa(i) + "-0192f1a2-1111-7222-8333-944445555666"
		exp := int64(1793502974 + i)
		want := referenceImageSignature(key, imageID, exp)
		if got := computeImageSignature(key, imageID, exp); !bytes.Equal(got, want) {
			t.Fatalf("computeImageSignature mismatch at %d", i)
		}
		// 1 回目は計算、2 回目はキャッシュから
		for range 2 {
			if got := imageSignature(key, imageID, exp); !bytes.Equal(got, want) {
				t.Fatalf("imageSignature mismatch at %d", i)
			}
		}
	}
}

func TestImageURLExp(t *testing.T) {
	now := time.Unix(1793416574, 0)
	exp := imageURLExp(now)
	lifetime := exp - now.Unix()
	if exp%imageURLExpStep != 0 || lifetime < int64(imageURLLifetime/time.Second) || lifetime >= int64(imageURLLifetime/time.Second)+imageURLExpStep {
		t.Fatalf("unexpected exp %d (lifetime %d)", exp, lifetime)
	}
	if imageURLExp(now.Add(time.Second)) != exp {
		t.Fatalf("exp should stay the same within a step")
	}
}
