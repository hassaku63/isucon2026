package isureal

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"strconv"
	"sync"
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
	base := int64((imageURLLifetime + imageURLExpProbe) / time.Second)
	if exp%imageURLExpStep != 0 || lifetime < base || lifetime >= base+imageURLExpStep {
		t.Fatalf("unexpected exp %d (lifetime %d)", exp, lifetime)
	}
	if imageURLExp(now.Add(time.Second)) != exp {
		t.Fatalf("exp should stay the same within a step")
	}
}

// 同じ署名を多数のゴルーチンが同時に求めても、全員が参照実装と同じ値を受け取る
func TestImageSignatureConcurrentMatchesReference(t *testing.T) {
	resetImageSignatureCache()
	type tc struct {
		key     []byte
		imageID string
		exp     int64
		want    []byte
	}
	cases := make([]tc, 8)
	for i := range cases {
		key := make([]byte, sha256.Size)
		rand.Read(key)
		imageID := "c" + strconv.Itoa(i) + "-0192f1a2-1111-7222-8333-944445555666"
		exp := int64(1793503000 + i*60)
		cases[i] = tc{key, imageID, exp, referenceImageSignature(key, imageID, exp)}
	}
	var wg sync.WaitGroup
	errs := make(chan string, 64*len(cases))
	for range 64 {
		for i := range cases {
			c := cases[i]
			wg.Go(func() {
				if got := imageSignature(c.key, c.imageID, c.exp); !bytes.Equal(got, c.want) {
					errs <- c.imageID
				}
			})
		}
	}
	wg.Wait()
	close(errs)
	for id := range errs {
		t.Fatalf("concurrent imageSignature mismatch for %s", id)
	}
	imageSignatureInflight.Lock()
	n := len(imageSignatureInflight.m)
	imageSignatureInflight.Unlock()
	if n != 0 {
		t.Fatalf("inflight map not empty: %d", n)
	}
}
