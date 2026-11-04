package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func newBox(t *testing.T) *Box {
	t.Helper()
	b, err := New(GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := newBox(t)
	for _, want := range [][]byte{[]byte("database password"), {}, bytes.Repeat([]byte{0, 255}, 4096)} {
		got, err := b.Open(b.Seal(want))
		if err != nil {
			t.Fatalf("open %d bytes: %v", len(want), err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestSealUsesNewNonceAndHidesPlaintext(t *testing.T) {
	b := newBox(t)
	plaintext := []byte("database password")
	first, second := b.Seal(plaintext), b.Seal(plaintext)
	if bytes.Equal(first, second) {
		t.Fatal("sealing twice gave the same output")
	}
	if bytes.Contains(first, plaintext) {
		t.Fatal("sealed value contains the plaintext")
	}
}

// The stored format is nonce‖ciphertext. Values already in a database depend
// on it, so check it against plain AES-GCM rather than against Open.
func TestStoredFormat(t *testing.T) {
	encoded := GenerateKey()
	b, err := New(encoded)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := base64.StdEncoding.DecodeString(encoded)
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)

	sealed := b.Seal([]byte("hello"))
	n := aead.NonceSize()
	got, err := aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestOpenRejectsChangedValues(t *testing.T) {
	b := newBox(t)
	sealed := b.Seal([]byte("secret"))
	for i := range sealed {
		changed := bytes.Clone(sealed)
		changed[i] ^= 1
		if _, err := b.Open(changed); !errors.Is(err, ErrOpen) {
			t.Fatalf("byte %d changed: got error %v", i, err)
		}
	}
	for _, short := range [][]byte{nil, {}, sealed[:5], sealed[:len(sealed)-1]} {
		if _, err := b.Open(short); !errors.Is(err, ErrOpen) {
			t.Fatalf("%d bytes: got error %v", len(short), err)
		}
	}
}

func TestOpenRejectsOtherKey(t *testing.T) {
	sealed := newBox(t).Seal([]byte("secret"))
	if _, err := newBox(t).Open(sealed); !errors.Is(err, ErrOpen) {
		t.Fatalf("got error %v", err)
	}
}

func TestNewRejectsBadKeys(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString
	for name, key := range map[string]string{
		"empty":      "",
		"not base64": "not-base64-!!!",
		"16 bytes":   enc(make([]byte, 16)),
		"31 bytes":   enc(make([]byte, 31)),
		"33 bytes":   enc(make([]byte, 33)),
		"url form":   strings.Repeat("-", 43) + "=",
	} {
		if _, err := New(key); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestNewErrorDoesNotContainKey(t *testing.T) {
	for _, key := range []string{"sup3rs3cretvalue", "sup3rs3cret!value"} {
		_, err := New(key)
		if err == nil {
			t.Fatalf("%q: no error", key)
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Fatalf("error contains the key: %v", err)
		}
	}
}

func TestGenerateKeyIsNewEachTime(t *testing.T) {
	if GenerateKey() == GenerateKey() {
		t.Fatal("two generated keys are the same")
	}
}
