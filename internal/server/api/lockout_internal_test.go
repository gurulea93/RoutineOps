package api

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// M6-регресс: ключ мапы усекается (гигантский email больше не оседает ~1MB-ключом),
// и число ключей не превышает потолок при флуде уникальными адресами (pre-auth OOM).
func TestLoginLimiter_KeyTruncatedAndCapped(t *testing.T) {
	l := newLoginLimiter(5, time.Minute, time.Minute)
	now := time.Now()

	l.fail(strings.Repeat("a", 5000), now)
	for k := range l.attempts {
		if len(k) > maxLockKeyLen {
			t.Fatalf("ключ длиной %d не усечён (макс %d)", len(k), maxLockKeyLen)
		}
	}

	// Флуд уникальными свежими ключами — мапа не должна расти безгранично.
	for i := 0; i < maxLockKeys+500; i++ {
		l.fail(fmt.Sprintf("user-%d@flood.test", i), now)
	}
	if len(l.attempts) > maxLockKeys {
		t.Fatalf("мапа attempts выросла до %d, потолок %d", len(l.attempts), maxLockKeys)
	}
}
