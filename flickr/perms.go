package flickr

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Perms is a Flickr permission level. The order matters: each level includes
// the ones below it.
type Perms int

// Permission levels, with the numbers flickr.reflection.getMethodInfo uses.
const (
	PermsNone Perms = iota
	PermsRead
	PermsWrite
	PermsDelete
)

var permsNames = []string{"none", "read", "write", "delete"}

func (p Perms) String() string {
	if p < PermsNone || p > PermsDelete {
		return fmt.Sprintf("Perms(%d)", int(p))
	}
	return permsNames[p]
}

// ParsePerms parses "none", "read", "write" or "delete".
func ParsePerms(s string) (Perms, error) {
	for i, name := range permsNames {
		if strings.EqualFold(strings.TrimSpace(s), name) {
			return Perms(i), nil
		}
	}
	return PermsNone, fmt.Errorf("unknown permission %q (want read, write or delete)", s)
}

// MarshalJSON writes the name, so token.json stays readable.
func (p Perms) MarshalJSON() ([]byte, error) { return json.Marshal(p.String()) }

// UnmarshalJSON reads the name.
func (p *Perms) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	v, err := ParsePerms(s)
	if err != nil {
		return err
	}
	*p = v
	return nil
}
