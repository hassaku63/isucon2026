package jpegcheck

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
)

type result struct {
	width, height int
	err           string
	panicked      bool
}

func decodeWithStd(data []byte) (res result) {
	defer func() {
		if r := recover(); r != nil {
			res = result{panicked: true}
		}
	}()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return result{err: err.Error()}
	}
	return result{width: img.Bounds().Dx(), height: img.Bounds().Dy()}
}

func validate(data []byte) (res result) {
	defer func() {
		if r := recover(); r != nil {
			res = result{panicked: true}
		}
	}()
	w, h, err := Validate(bytes.NewReader(data))
	if err != nil {
		return result{err: err.Error()}
	}
	return result{width: w, height: h}
}

func corpus(t *testing.T) map[string][]byte {
	files := map[string][]byte{}
	paths, _ := filepath.Glob(filepath.Join(runtime.GOROOT(), "src", "image", "testdata", "*.jpeg"))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		files[filepath.Base(p)] = data
	}
	if len(files) == 0 {
		t.Log("GOROOT の testdata が見つからない。生成した画像だけで試す")
	}
	// ベンチマーカーが投稿した画像などを足して試すときは、JPEGCHECK_EXTRA_DIR に置き場所を渡す（リポジトリには入れない）
	if dir := os.Getenv("JPEGCHECK_EXTRA_DIR"); dir != "" {
		extra, _ := filepath.Glob(filepath.Join(dir, "*.jpg"))
		for _, p := range extra {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			files[filepath.Base(p)] = data
		}
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for _, size := range [][2]int{{1, 1}, {7, 9}, {64, 48}, {333, 517}, {512, 512}} {
		rgba := image.NewRGBA(image.Rect(0, 0, size[0], size[1]))
		gray := image.NewGray(rgba.Bounds())
		for i := range rgba.Pix {
			rgba.Pix[i] = uint8(rng.UintN(256))
		}
		for y := range size[1] {
			for x := range size[0] {
				gray.SetGray(x, y, color.Gray{Y: uint8(x ^ y)})
			}
		}
		for _, q := range []int{1, 50, 100} {
			for name, img := range map[string]image.Image{"rgba": rgba, "gray": gray} {
				var buf bytes.Buffer
				if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
					t.Fatal(err)
				}
				files[fmt.Sprintf("gen-%s-%dx%d-q%d", name, size[0], size[1], q)] = buf.Bytes()
			}
		}
	}
	return files
}

func check(t *testing.T, name string, data []byte) bool {
	t.Helper()
	want, got := decodeWithStd(data), validate(data)
	if want != got {
		t.Errorf("%s: image/jpeg = %+v, Validate = %+v", name, want, got)
		return false
	}
	return true
}

// 壊し方の乱数の種。JPEGCHECK_SEED で変えられる
var seed = func() uint64 {
	v, _ := strconv.ParseUint(os.Getenv("JPEGCHECK_SEED"), 10, 64)
	return v
}()

// 正常な画像と、壊した画像（切り詰め、バイトの書き換え・挿入・削除）で、image/jpeg.Decode と結果が一致する
func TestValidateMatchesStdDecode(t *testing.T) {
	stats := map[string]int{}
	files := corpus(t)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		data := files[name]
		rng := rand.New(rand.NewPCG(seed, uint64(len(data))))
		if !check(t, name, data) {
			continue
		}
		step := max(1, len(data)/100)
		for n := 0; n < len(data); n += step {
			check(t, fmt.Sprintf("%s truncated to %d", name, n), data[:n])
		}
		for i := range 300 {
			mutated := bytes.Clone(data)
			switch i % 4 {
			case 0:
				mutated[rng.IntN(len(mutated))] = uint8(rng.UintN(256))
			case 1:
				mutated[rng.IntN(len(mutated))] ^= 1 << rng.UintN(8)
			case 2:
				p := rng.IntN(len(mutated))
				mutated = append(mutated[:p:p], append([]byte{uint8(rng.UintN(256))}, data[p:]...)...)
			case 3:
				p := rng.IntN(len(mutated))
				mutated = append(mutated[:p:p], data[p+1:]...)
			}
			r := decodeWithStd(mutated)
			switch {
			case r.panicked:
				stats["panic"]++
			case r.err != "":
				stats["error"]++
			default:
				stats["ok"]++
			}
			check(t, fmt.Sprintf("%s mutation %d", name, i), mutated)
		}
	}
	t.Logf("壊した画像の image/jpeg での結果: %v", stats)
}

func BenchmarkDecode(b *testing.B) {
	data := benchImage(b)
	for b.Loop() {
		if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidate(b *testing.B) {
	data := benchImage(b)
	for b.Loop() {
		if _, _, err := Validate(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

// JPEGCHECK_BENCH_FILE があればその画像で測る。なければノイズの画像（係数が多く、ハフマン復号の割合が実際より大きい）
func benchImage(b *testing.B) []byte {
	if p := os.Getenv("JPEGCHECK_BENCH_FILE"); p != "" {
		data, err := os.ReadFile(p)
		if err != nil {
			b.Fatal(err)
		}
		return data
	}
	rng := rand.New(rand.NewPCG(5, 6))
	img := image.NewRGBA(image.Rect(0, 0, 1080, 1440))
	for i := range img.Pix {
		img.Pix[i] = uint8(rng.UintN(256))
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		b.Fatal(err)
	}
	return buf.Bytes()
}
