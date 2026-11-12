package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration that marshals to and from human strings such as
// "30m", "24h" or "30d". It extends time.ParseDuration with day ("d") and
// week ("w") units, which the config format uses for retention windows.
type Duration time.Duration

// ParseDuration parses a duration string, allowing d (day) and w (week) units
// in addition to the units supported by time.ParseDuration.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// Split off a trailing d/w unit; everything else goes to time.ParseDuration.
	if n := len(s); n > 1 {
		switch s[n-1] {
		case 'd':
			v, err := strconv.ParseFloat(s[:n-1], 64)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q", s)
			}
			return time.Duration(v * float64(24*time.Hour)), nil
		case 'w':
			v, err := strconv.ParseFloat(s[:n-1], 64)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q", s)
			}
			return time.Duration(v * float64(7*24*time.Hour)), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q — use values like 30s, 10m, 24h or 30d", s)
	}
	return d, nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		// Allow plain numbers as seconds for convenience.
		var n float64
		if err2 := unmarshal(&n); err2 != nil {
			return fmt.Errorf("duration must be a string like \"30m\"")
		}
		*d = Duration(time.Duration(n * float64(time.Second)))
		return nil
	}
	v, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) { return []byte(strconv.Quote(d.String())), nil }

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return err
	}
	v, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// D returns the value as time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// String renders the duration compactly, using d/w for large values.
func (d Duration) String() string {
	v := time.Duration(d)
	if v == 0 {
		return "0s"
	}
	if v%(7*24*time.Hour) == 0 {
		return strconv.FormatInt(int64(v/(7*24*time.Hour)), 10) + "w"
	}
	if v%(24*time.Hour) == 0 {
		return strconv.FormatInt(int64(v/(24*time.Hour)), 10) + "d"
	}
	return v.String()
}

// ParseBytes parses a memory size such as "2GiB", "512MiB", "1.5GB" or a plain
// byte count. Both binary (KiB, MiB, ...) and decimal (KB, MB, ...) units are
// accepted.
func ParseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	units := []struct {
		suffix string
		mult   float64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3},
		{"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10},
		{"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			num := strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			v, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid size %q", s)
			}
			return int64(v * u.mult), nil
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q — use values like 512MiB or 2GiB", s)
	}
	return v, nil
}