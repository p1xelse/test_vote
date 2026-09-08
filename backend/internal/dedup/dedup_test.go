package dedup_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/dneumoin/test_vote/backend/internal/dedup"
)

const testSecret = "test-secret-at-least-16-chars"

func TestSignerRoundTrip(t *testing.T) {
	t.Parallel()

	signer := dedup.NewSigner(testSecret)
	voterID, cookie := signer.Issue()

	parsed, ok := signer.Verify(cookie)
	require.True(t, ok)
	require.Equal(t, voterID, parsed)
}

func TestSignerRejectsForgedCookies(t *testing.T) {
	t.Parallel()

	signer := dedup.NewSigner(testSecret)
	_, cookie := signer.Issue()

	cases := map[string]string{
		"пустая строка":      "",
		"без подписи":        strings.Split(cookie, ".")[0],
		"мусор":              "not-a-cookie.at-all",
		"чужая подпись":      strings.Split(cookie, ".")[0] + ".AAAAAAAAAAAAAAAAAAAAAA",
		"подменённый id":     "AAAAAAAAAAAAAAAAAAAAAA." + strings.Split(cookie, ".")[1],
		"подпись от другого": mustOtherSignature(),
		"лишний разделитель": cookie + ".extra",
	}

	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, ok := signer.Verify(value)
			require.False(t, ok)
		})
	}
}

func mustOtherSignature() string {
	other := dedup.NewSigner("completely-different-secret")
	_, cookie := other.Issue()

	return cookie
}

func TestKeyIsStableAndPollScoped(t *testing.T) {
	t.Parallel()

	voterID := uuid.New()
	identity := dedup.CookieIdentity(voterID)
	firstPoll, secondPoll := uuid.New(), uuid.New()

	require.Equal(t, dedup.Key(firstPoll, identity), dedup.Key(firstPoll, identity),
		"один и тот же зритель в одном опросе должен давать один ключ")
	require.NotEqual(t, dedup.Key(firstPoll, identity), dedup.Key(secondPoll, identity),
		"в разных опросах зритель должен голосовать независимо")
}

func TestKeyStartsWithPollID(t *testing.T) {
	t.Parallel()

	pollID := uuid.New()
	key := dedup.Key(pollID, dedup.CookieIdentity(uuid.New()))

	// Префикс с id опроса нужен для отладки и очистки; хэш стоит в конце,
	// чтобы ключи расходились по слотам кластера.
	require.True(t, strings.HasPrefix(key, "vote:d:"+pollID.String()+":"), key)
}

func TestCookieAndFingerprintIdentitiesDiffer(t *testing.T) {
	t.Parallel()

	pollID := uuid.New()
	cookie := dedup.CookieIdentity(uuid.New())
	fingerprint := dedup.FingerprintIdentity("10.0.0.1", "Mozilla/5.0")

	require.True(t, cookie.FromCookie)
	require.False(t, fingerprint.FromCookie)
	require.NotEqual(t, dedup.Key(pollID, cookie), dedup.Key(pollID, fingerprint))
}

func TestNormalizeIP(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"IPv4 огрубляется до /24", "203.0.113.42", "203.0.113.0/24"},
		{"соседний адрес той же подсети", "203.0.113.99", "203.0.113.0/24"},
		{"адрес с портом", "203.0.113.42:51234", "203.0.113.0/24"},
		{"IPv6 огрубляется до /64", "2001:db8::1", "2001:db8::/64"},
		{"мусор возвращается как есть", "not-an-ip", "not-an-ip"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, dedup.NormalizeIP(tc.input))
		})
	}
}

func TestFingerprintGroupsSubnetButSplitsUserAgent(t *testing.T) {
	t.Parallel()

	pollID := uuid.New()
	sameSubnet := dedup.Key(pollID, dedup.FingerprintIdentity("203.0.113.7", "UA"))
	neighbour := dedup.Key(pollID, dedup.FingerprintIdentity("203.0.113.9", "UA"))
	otherUA := dedup.Key(pollID, dedup.FingerprintIdentity("203.0.113.7", "Other UA"))

	require.Equal(t, sameSubnet, neighbour)
	require.NotEqual(t, sameSubnet, otherUA)
}

func BenchmarkKey(b *testing.B) {
	pollID := uuid.New()
	identity := dedup.CookieIdentity(uuid.New())

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_ = dedup.Key(pollID, identity)
	}
}

func BenchmarkSignerVerify(b *testing.B) {
	signer := dedup.NewSigner(testSecret)
	_, cookie := signer.Issue()

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		if _, ok := signer.Verify(cookie); !ok {
			b.Fatal("подпись должна быть валидной")
		}
	}
}
