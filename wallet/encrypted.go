package wallet

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mirstar13/go-blockchain/ledger"
	"golang.org/x/crypto/scrypt"
)

const envelopeVersion = "ledger-wallet-scrypt-aes256gcm-v1"

type envelope struct {
	Version    string
	Salt       []byte
	Nonce      []byte
	Ciphertext []byte
}

var ErrDecrypt = errors.New("incorrect passphrase or damaged encrypted key")

func encryptionKey(password, salt []byte) ([]byte, error) {
	if len(password) == 0 || len(password) > 4096 {
		return nil, errors.New("passphrase must contain 1 to 4096 bytes")
	}
	return scrypt.Key(password, salt, 32768, 8, 1, 32)
}
func writeSecret(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

// SaveEncrypted writes a new authenticated envelope and never replaces a file.
func SaveEncrypted(path string, key ed25519.PrivateKey, password []byte) error {
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("invalid private key length")
	}
	if !bytes.Equal(key, ed25519.NewKeyFromSeed(key.Seed())) {
		return errors.New("inconsistent private key")
	}
	env := envelope{Version: envelopeVersion, Salt: make([]byte, 16), Nonce: make([]byte, 12)}
	if _, err := rand.Read(env.Salt); err != nil {
		return err
	}
	if _, err := rand.Read(env.Nonce); err != nil {
		return err
	}
	derived, err := encryptionKey(password, env.Salt)
	if err != nil {
		return err
	}
	defer clear(derived)
	block, err := aes.NewCipher(derived)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	env.Ciphertext = gcm.Seal(nil, env.Nonce, key.Seed(), []byte(env.Version))
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return writeSecret(path, append(data, '\n'))
}
func CreateEncrypted(path string, password []byte) (ledger.Address, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return ledger.Address{}, err
	}
	defer clear(key)
	if err = SaveEncrypted(path, key, password); err != nil {
		return ledger.Address{}, err
	}
	return Address(key)
}

// ReadSecret checks permissions on the same descriptor it reads, and bounds input.
func ReadSecret(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !privateFile(info) {
		return nil, errors.New("secret must be a regular file with platform-appropriate access permissions")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		clear(data)
		return nil, fmt.Errorf("secret file exceeds %d bytes", limit)
	}
	return data, nil
}
func LoadEncrypted(path string, password []byte) (ed25519.PrivateKey, error) {
	data, err := ReadSecret(path, 4096)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err = json.Unmarshal(data, &env); err != nil {
		return nil, ErrDecrypt
	}
	if env.Version != envelopeVersion {
		return nil, errors.New("unsupported encrypted key version")
	}
	// Fixed parameters and lengths prevent malicious envelopes selecting huge KDF costs.
	if len(env.Salt) != 16 || len(env.Nonce) != 12 || len(env.Ciphertext) != 48 {
		return nil, ErrDecrypt
	}
	derived, err := encryptionKey(password, env.Salt)
	if err != nil {
		return nil, err
	}
	defer clear(derived)
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	seed, err := gcm.Open(nil, env.Nonce, env.Ciphertext, []byte(env.Version))
	if err != nil {
		return nil, ErrDecrypt
	}
	defer clear(seed)
	return ed25519.NewKeyFromSeed(seed), nil
}
func SignCancellation(key ed25519.PrivateKey, request ledger.Cancellation) (ledger.Cancellation, error) {
	address, err := Address(key)
	if err != nil {
		return request, err
	}
	if address != request.Sender {
		return request, errors.New("signing key does not own this transaction")
	}
	copy(request.Signature[:], ed25519.Sign(key, request.SigningBytes()))
	return request, nil
}

// EncryptFile is the only plaintext import operation supported on Windows.
// It preserves the source and publishes an encrypted copy at a new path.
func EncryptFile(source, destination string, password []byte) (ledger.Address, error) {
	key, err := loadPlaintext(source)
	if err != nil {
		return ledger.Address{}, err
	}
	defer clear(key)
	if err = SaveEncrypted(destination, key, password); err != nil {
		return ledger.Address{}, err
	}
	return Address(key)
}
