// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package jpegcheck

// image/jpeg の dct.go から、復号に要る型だけを写した（逆 DCT は使わない）

const blockSize = 8 * 8

type block [blockSize]int32
