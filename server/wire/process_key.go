package wire

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-faster/city"
)

// ErrBadProcessKey is the error for a text that is not a process key.
var ErrBadProcessKey = errors.New("wire: bad process key")

// ProcessKey names a process across samples and rollups: the name and
// the ClickHouse cityHash64 of the command.
type ProcessKey struct {
	Name    string
	CmdHash uint64
}

// NewProcessKey makes the key of a process name and command.
func NewProcessKey(name, cmd string) ProcessKey {
	return ProcessKey{Name: name, CmdHash: city.CH64([]byte(cmd))}
}

// ParseProcessKey reads the text form of a key.
func ParseProcessKey(s string) (ProcessKey, error) {
	hash, name, ok := strings.Cut(s, ".")
	if !ok || len(hash) != 16 || name == "" {
		return ProcessKey{}, ErrBadProcessKey
	}
	h, err := strconv.ParseUint(hash, 16, 64)
	if err != nil {
		return ProcessKey{}, fmt.Errorf("%w: hash", ErrBadProcessKey)
	}
	raw, err := base64.RawURLEncoding.DecodeString(name)
	if err != nil || len(raw) == 0 {
		return ProcessKey{}, fmt.Errorf("%w: name", ErrBadProcessKey)
	}
	return ProcessKey{Name: string(raw), CmdHash: h}, nil
}

// String gives "<16 hex digits of CmdHash>.<base64url(Name), no padding>".
// The text has no "/" and always one ".".
func (k ProcessKey) String() string {
	return fmt.Sprintf("%016x.%s", k.CmdHash, base64.RawURLEncoding.EncodeToString([]byte(k.Name)))
}

func (k ProcessKey) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

func (k *ProcessKey) UnmarshalText(b []byte) error {
	v, err := ParseProcessKey(string(b))
	if err != nil {
		return err
	}
	*k = v
	return nil
}
