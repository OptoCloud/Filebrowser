package auth

import (
	"bufio"
	"context"
	"crypto/sha1" // #nosec G505 -- the breach corpus is indexed by SHA-1; nothing is protected by it here
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// PasswordPolicy decides whether a new password is acceptable. Beyond the
// length rule of ValidatePassword it refuses what is guessed first: the
// account's own name, runs and repeats, the passwords everyone uses, and (if
// Pwned) passwords found in known data breaches.
type PasswordPolicy struct {
	// Pwned asks the Pwned Passwords service whether the password is in a
	// known breach. Only the first five hex digits of the password's SHA-1
	// leave the server (k-anonymity), with padding so that the size of the
	// answer says nothing either. If the service cannot be reached, the
	// password is accepted.
	Pwned bool
	// RangeURL is the service's range endpoint; set for tests.
	RangeURL string
	Client   *http.Client
}

const pwnedRangeURL = "https://api.pwnedpasswords.com/range/"

// common are the usual passwords long enough to pass the length rule.
var common = map[string]bool{}

func init() {
	for _, p := range strings.Fields(`
		1234567890 12345678910 123456789a 1234567891 0123456789 9876543210 0987654321 1q2w3e4r5t
		1q2w3e4r5t6y qwertyuiop qwerty12345 qwerty123456 qwertyuiop123 1qaz2wsx3edc qazwsxedcrfv
		password123 password1234 password12345 passwordpassword password01 password11 passw0rd123
		p@ssw0rd123 p@ssword123 mypassword1 iloveyou123 iloveyou12 iloveyou1234 welcome123
		welcome1234 letmein123 letmein1234 admin12345 admin123456 administrator changeme123
		changeme1234 football123 baseball123 basketball superman123 batman12345 starwars123
		princess123 sunshine123 monkey12345 dragon12345 master12345 michael123 jennifer123
		abc123456789 abcd123456 abcdefghij abcdefghijk aaaaaaaaaa asdfghjkl1 asdfghjkl123
		zxcvbnm123 1111111111 0000000000 1234512345 1122334455 9999999999 google12345
		trustno1234 whatever123 computer123 internet123 samsung123 filebrowser correcthorsebatterystaple`) {
		common[p] = true
	}
}

// Check validates a new password for the account named username ("" if unknown).
func (p *PasswordPolicy) Check(ctx context.Context, pw, username string) error {
	if err := ValidatePassword(pw); err != nil {
		return err
	}
	lower := strings.ToLower(pw)
	if u := strings.ToLower(username); len(u) >= 3 && strings.Contains(lower, u) {
		return errors.New("the password must not contain your username")
	}
	if common[lower] || monotonous(lower) {
		return errors.New("this password is too easy to guess")
	}
	if p != nil && p.Pwned {
		if n := p.pwned(ctx, pw); n > 0 {
			return fmt.Errorf("this password has appeared in %d known data breaches: choose another", n)
		}
	}
	return nil
}

// monotonous reports a password that is one character repeated, a short
// pattern repeated, or a run up or down the digits or the alphabet.
func monotonous(pw string) bool {
	r := []rune(pw)
	for period := 1; period <= 4 && period*2 <= len(r); period++ {
		repeat := true
		for i := period; i < len(r); i++ {
			if r[i] != r[i-period] {
				repeat = false
				break
			}
		}
		if repeat {
			return true
		}
	}
	for _, step := range []rune{1, -1} {
		run := true
		for i := 1; i < len(r); i++ {
			if !(unicode.IsDigit(r[i]) || unicode.IsLetter(r[i])) || r[i]-r[i-1] != step {
				run = false
				break
			}
		}
		if run {
			return true
		}
	}
	return false
}

// pwned returns how often the password appears in known breaches, 0 if
// never or if that could not be found out.
func (p *PasswordPolicy) pwned(ctx context.Context, pw string) int {
	sum := sha1.Sum([]byte(pw)) // #nosec G401 -- see the import
	digest := strings.ToUpper(hex.EncodeToString(sum[:]))
	base := p.RangeURL
	if base == "" {
		base = pwnedRangeURL
	}
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+digest[:5], nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Add-Padding", "true")
	req.Header.Set("User-Agent", "filebrowser")
	res, err := client.Do(req)
	if err != nil {
		slog.Warn("breached password check unavailable", "err", err)
		return 0
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		slog.Warn("breached password check unavailable", "status", res.StatusCode)
		return 0
	}
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		suffix, count, ok := strings.Cut(strings.TrimSpace(sc.Text()), ":")
		if ok && suffix == digest[5:] {
			n, _ := strconv.Atoi(count)
			return n // padding lines have a count of 0
		}
	}
	return 0
}
