package voter_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/dneumoin/test_vote/backend/internal/dedup"
	"github.com/dneumoin/test_vote/backend/internal/http/voter"
)

const cookieName = "vid"

func newResolver() *voter.Resolver {
	return voter.New(dedup.NewSigner("test-secret-at-least-16-chars"), cookieName, time.Hour, false)
}

func newContext(cookie *http.Cookie) (echo.Context, *httptest.ResponseRecorder) {
	request := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	request.Header.Set("User-Agent", "TestAgent/1.0")
	if cookie != nil {
		request.AddCookie(cookie)
	}

	recorder := httptest.NewRecorder()

	return echo.New().NewContext(request, recorder), recorder
}

func issuedCookie(t *testing.T, recorder *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()

	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == cookieName {
			return cookie
		}
	}

	t.Fatal("сервис не выдал куку голосующего")

	return nil
}

func TestIssueSetsCookieOnce(t *testing.T) {
	t.Parallel()

	resolver := newResolver()

	c, recorder := newContext(nil)
	resolver.Issue(c)
	cookie := issuedCookie(t, recorder)

	require.True(t, cookie.HttpOnly, "кука не нужна скриптам на странице")
	require.Equal(t, http.SameSiteLaxMode, cookie.SameSite)

	// Повторный заход с валидной кукой не должен её перевыпускать: иначе
	// зритель получал бы новую личность на каждой перезагрузке страницы.
	c, recorder = newContext(cookie)
	resolver.Issue(c)
	require.Empty(t, recorder.Result().Cookies())
}

func TestIdentifyUsesCookieWhenPresent(t *testing.T) {
	t.Parallel()

	resolver := newResolver()

	c, recorder := newContext(nil)
	resolver.Issue(c)
	cookie := issuedCookie(t, recorder)

	c, _ = newContext(cookie)
	identity := resolver.Identify(c)

	require.True(t, identity.FromCookie)

	// Тот же зритель на другом запросе — та же личность.
	c, _ = newContext(cookie)
	require.Equal(t, identity, resolver.Identify(c))
}

func TestIdentifyFallsBackToFingerprint(t *testing.T) {
	t.Parallel()

	resolver := newResolver()

	c, _ := newContext(nil)
	identity := resolver.Identify(c)

	require.False(t, identity.FromCookie,
		"без куки зритель опознаётся по подсети и User-Agent")
}

// TestIdentifyIgnoresForgedCookie — ключевая проверка: подделанная кука не даёт
// новой личности, иначе дедупликация обходилась бы одним заголовком.
func TestIdentifyIgnoresForgedCookie(t *testing.T) {
	t.Parallel()

	resolver := newResolver()

	c, _ := newContext(&http.Cookie{Name: cookieName, Value: "forged.value"})
	identity := resolver.Identify(c)

	require.False(t, identity.FromCookie)
}
