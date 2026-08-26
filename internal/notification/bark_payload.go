package notification

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"io"
	"unicode"
)

const (
	barkSound          = "alarm.caf"
	barkIVRandomBytes  = 9
	barkAES128KeyBytes = 16
	barkAES192KeyBytes = 24
	barkAES256KeyBytes = 32
)

type barkPlainPayload struct {
	DeviceKey string `json:"device_key"`
	Title     string `json:"title"`
	Body      string `json:"body"`
}

type barkEncryptedPayload struct {
	deviceKey string
	title     string
	body      string
	key       []byte
	random    io.Reader
}

type barkEncryptedContent struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Sound string `json:"sound"`
}

type barkEncryptedRequest struct {
	DeviceKey  string `json:"device_key"`
	Ciphertext string `json:"ciphertext"`
	IV         string `json:"iv"`
}

func newBarkPayload(deviceKey, encryptionKey, title, body string) any {
	if encryptionKey == "" {
		return barkPlainPayload{
			DeviceKey: deviceKey,
			Title:     title,
			Body:      body,
		}
	}

	return barkEncryptedPayload{
		deviceKey: deviceKey,
		title:     title,
		body:      body,
		key:       []byte(encryptionKey),
		random:    rand.Reader,
	}
}

func (payload barkEncryptedPayload) MarshalJSON() ([]byte, error) {
	plaintext, err := json.Marshal(barkEncryptedContent{
		Title: payload.title,
		Body:  payload.body,
		Sound: barkSound,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal Bark plaintext: %w", err)
	}

	ivBytes := make([]byte, barkIVRandomBytes)
	if _, err = io.ReadFull(payload.random, ivBytes); err != nil {
		return nil, fmt.Errorf("generate Bark encryption IV: %w", err)
	}
	iv := base64.RawURLEncoding.EncodeToString(ivBytes)

	gcm, err := newBarkCipher(payload.key)
	if err != nil {
		return nil, fmt.Errorf("create Bark encryption cipher: %w", err)
	}

	// Bark uses the 12 ASCII bytes of the base64url IV as the GCM nonce.
	ciphertext := gcm.Seal(nil, []byte(iv), plaintext, nil)

	request, err := json.Marshal(barkEncryptedRequest{
		DeviceKey:  payload.deviceKey,
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
		IV:         iv,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal encrypted Bark request: %w", err)
	}

	return request, nil
}

func newBarkCipher(key []byte) (cipher.AEAD, error) {
	return newBarkCipherWith(key, cipher.NewGCM)
}

func newBarkCipherWith(key []byte, newGCM func(cipher.Block) (cipher.AEAD, error)) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES block: %w", err)
	}
	gcm, err := newGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM mode: %w", err)
	}

	return gcm, nil
}

func validBarkEncryptionKey(key string) bool {
	if key == "" {
		return true
	}

	switch len(key) {
	case barkAES128KeyBytes, barkAES192KeyBytes, barkAES256KeyBytes:
	default:
		return false
	}

	for _, character := range key {
		if character > unicode.MaxASCII {
			return false
		}
	}

	return true
}
