// Copyright 2026 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package collation constructs byte-ordered comparison keys. Keys are opaque:
// they do not contain the original value or their comparison domain.
package collation

import (
	"errors"
	"unicode/utf8"
)

// Domain is resolved from schema metadata, never from a key's contents.
type Domain uint8

const (
	Raw Domain = iota
	UTF8MB4Bin
	UTF8MB4GeneralCI
)

var (
	ErrDomain = errors.New("unsupported collation key domain")
	ErrUTF8   = errors.New("invalid UTF-8 in collation key")
	ErrKey    = errors.New("invalid collation key encoding")
)

// Key returns raw input unchanged for Raw (borrowed, without allocation).
// Otherwise it appends the PAD SPACE v1 key to scratch[:0]. The result is
// borrowed until scratch is reused. Scratch must not overlap value. Invalid
// text/domain leaves scratch unchanged.
// Unlike the legacy aggregate comparator, new transformed keys reject malformed
// UTF-8; no synthetic invalid-byte identity is persisted.
func (d Domain) Key(scratch, value []byte) ([]byte, error) {
	if d == Raw {
		return value, nil
	}
	if d != UTF8MB4Bin && d != UTF8MB4GeneralCI {
		return nil, ErrDomain
	}
	if !utf8.Valid(value) {
		return nil, ErrUTF8
	}
	out := scratch[:0]
	spaces := 0
	for len(value) != 0 {
		w, n := d.next(value)
		value = value[n:]
		if w == 0x20 {
			spaces++
			continue
		}
		// An interior space sorts on the side of the implicit PAD SPACE
		// terminator determined by the next non-space weight. This also
		// orders unequal runs of spaces correctly without full-width padding.
		space := byte(0x23)
		if w < 0x20 {
			space = 0x21
		}
		for ; spaces > 0; spaces-- {
			out = append(out, space)
		}
		switch {
		case w < 32:
			out = append(out, byte(w+1))
		case w < 128:
			out = append(out, byte(w+3))
		default:
			out = utf8.AppendRune(out, rune(w))
		}
	}
	// Trailing spaces are implicit. The terminator is in the middle of
	// the weight order, unlike a zero terminator or a shorter byte slice.
	return append(out, 0x22), nil
}

func (d Domain) next(value []byte) (uint32, int) {
	if d == UTF8MB4GeneralCI {
		return NextGeneralCIWeight(value)
	}
	r, n := utf8.DecodeRune(value)
	return uint32(r), n
}

// ValidateKey checks canonical framing and weight bounds without decoding into
// user text. The caller has already removed the outer tuple escaping.
func (d Domain) ValidateKey(key []byte) error {
	if d == Raw {
		return nil
	}
	if d != UTF8MB4Bin && d != UTF8MB4GeneralCI {
		return ErrDomain
	}
	space := byte(0)
	for i := 0; i < len(key); {
		m := key[i]
		i++
		switch {
		case m == 0x22:
			if i == len(key) && space == 0 {
				return nil
			}
			return ErrKey
		case m == 0x21 || m == 0x23:
			if space != 0 && space != m {
				return ErrKey
			}
			space = m
		case m >= 1 && m <= 0x20:
			if space == 0x23 {
				return ErrKey
			}
			space = 0
		case m >= 0x24 && m <= 0x82:
			if space == 0x21 || (d == UTF8MB4GeneralCI && utf8mb4GeneralCIWeight(rune(m-3)) != uint32(m-3)) {
				return ErrKey
			}
			space = 0
		case m >= 0xc2:
			r, n := utf8.DecodeRune(key[i-1:])
			if n == 1 || (d == UTF8MB4GeneralCI && (r > 0xffff || utf8mb4GeneralCIWeight(r) != uint32(r))) || space == 0x21 {
				return ErrKey
			}
			i += n - 1
			space = 0
		default:
			return ErrKey
		}
	}
	return ErrKey
}
