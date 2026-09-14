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

package types

import (
	"bytes"
	"fmt"

	"github.com/matrixorigin/matrixone/pkg/common/collation"
)

// KeyFormat is relation/index metadata, not part of a tuple field. A missing
// format is LegacyKeyFormat even when a column has explicit collation metadata.
type KeyFormat uint8

const (
	LegacyKeyFormat KeyFormat = iota
	PADSpaceKeyV1
)

// StringKeyPart is an immutable, schema-resolved encoder shared by stored key
// writers and query probes. Storage coercion and character-prefix extraction
// must happen before Key/Encode; they never truncate an over-width probe.
type StringKeyPart struct {
	domain collation.Domain
}

func ResolveStringKeyPart(typ Type, format KeyFormat) (StringKeyPart, error) {
	if format != LegacyKeyFormat && format != PADSpaceKeyV1 {
		return StringKeyPart{}, fmt.Errorf("unsupported collation key format %d", format)
	}
	switch typ.Oid {
	case T_binary, T_varbinary, T_blob:
		return StringKeyPart{}, nil
	case T_char, T_varchar, T_text:
	default:
		return StringKeyPart{}, fmt.Errorf("not a text or binary key part: %s", typ.Oid)
	}
	if format == LegacyKeyFormat {
		return StringKeyPart{}, nil
	}
	switch typ.Charset {
	case CharsetLegacy, CharsetBinary:
		return StringKeyPart{}, nil
	case CharsetUTF8MB4Bin:
		return StringKeyPart{domain: collation.UTF8MB4Bin}, nil
	case CharsetUTF8:
		return StringKeyPart{domain: collation.UTF8MB4GeneralCI}, nil
	default:
		return StringKeyPart{}, collation.ErrDomain
	}
}

func (part StringKeyPart) Transformed() bool { return part.domain != collation.Raw }

// Key returns a borrowed key; copy it before reusing scratch or the input.
// Already transformed expressions have binary type metadata and take the raw
// path. Callers must not relabel an opaque key with its source text's charset.
func (part StringKeyPart) Key(scratch, value []byte) ([]byte, error) {
	return part.domain.Key(scratch, value)
}

// Encode appends one field using the unchanged tuple string framing. It returns
// the scratch buffer for reuse. No partial field is appended on admission or
// fixed-capacity errors; an already-failed packer remains failed.
func (part StringKeyPart) Encode(p *Packer, scratch, value []byte) ([]byte, error) {
	if err := p.Err(); err != nil {
		return scratch, err
	}
	key, err := part.Key(scratch, value)
	if err != nil {
		return scratch, err
	}
	if part.Transformed() {
		scratch = key
	}
	if p.fixed {
		available := cap(p.buffer) - len(p.buffer)
		// Avoid addition overflow for arbitrarily large input.
		if available < 3 || len(key) > available-3 || bytes.Count(key, []byte{0}) > available-3-len(key) {
			return scratch, ErrPackerCapacity
		}
	}
	p.EncodeStringType(key)
	return scratch, p.Err()
}

// DecodedStringKey distinguishes opaque weights from recoverable original
// bytes. Bytes is owned by the result, independent of the input tuple buffer.
type DecodedStringKey struct {
	Bytes  []byte
	Opaque bool
}

// Decode consumes exactly one field. It reverses tuple escaping, not collation:
// a transformed value can only be returned to SQL by fetching the original row.
func (part StringKeyPart) Decode(tuple []byte) (DecodedStringKey, int, error) {
	if len(tuple) < 3 || tuple[0] != stringTypeCode || tuple[1] != bytesCode {
		return DecodedStringKey{}, 0, collation.ErrKey
	}
	key := make([]byte, 0)
	for i := 2; i < len(tuple); i++ {
		b := tuple[i]
		if b != 0 {
			key = append(key, b)
			continue
		}
		if i+1 < len(tuple) && tuple[i+1] == 0xff {
			key = append(key, 0)
			i++
			continue
		}
		if err := part.domain.ValidateKey(key); err != nil {
			return DecodedStringKey{}, 0, err
		}
		return DecodedStringKey{Bytes: key, Opaque: part.Transformed()}, i + 1, nil
	}
	return DecodedStringKey{}, 0, collation.ErrKey
}
