// Package voter опознаёт зрителя по подписанной куке.
package voter

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/dneumoin/test_vote/backend/internal/dedup"
)

// Resolver выдаёт и читает куку голосующего.
type Resolver struct {
	signer     *dedup.Signer
	cookieName string
	cookieTTL  time.Duration
	secure     bool
}

// New создаёт резолвер. secure=false нужен для локального http.
func New(signer *dedup.Signer, cookieName string, cookieTTL time.Duration, secure bool) *Resolver {
	return &Resolver{signer: signer, cookieName: cookieName, cookieTTL: cookieTTL, secure: secure}
}

// Issue выдаёт куку зрителю, если её ещё нет.
//
// Вызывается на чтении опроса: зритель сначала открывает страницу по QR-коду
// и только потом жмёт кнопку, поэтому к моменту голосования кука уже есть.
func (r *Resolver) Issue(c echo.Context) {
	if cookie, err := c.Cookie(r.cookieName); err == nil {
		if _, ok := r.signer.Verify(cookie.Value); ok {
			return
		}
	}

	_, value := r.signer.Issue()
	c.SetCookie(r.newCookie(value))
}

// Identify возвращает личность зрителя для дедупликации.
//
// Если рабочей куки нет, зритель опознаётся по подсети и User-Agent. Это
// заметно грубее и за общим NAT может схлопнуть нескольких человек в одного,
// но выдавать свежую куку прямо в этом запросе нельзя: тогда любой клиент,
// который просто не хранит куки, голосовал бы сколько угодно раз.
func (r *Resolver) Identify(c echo.Context) dedup.Identity {
	if cookie, err := c.Cookie(r.cookieName); err == nil {
		if voterID, ok := r.signer.Verify(cookie.Value); ok {
			return dedup.CookieIdentity(voterID)
		}
	}

	// Куку всё равно ставим — следующий голос этого зрителя будет опознан точнее.
	_, value := r.signer.Issue()
	c.SetCookie(r.newCookie(value))

	return dedup.FingerprintIdentity(c.RealIP(), c.Request().UserAgent())
}

func (r *Resolver) newCookie(value string) *http.Cookie {
	return &http.Cookie{
		Name:  r.cookieName,
		Value: value,
		Path:  "/",
		// Кука не нужна скриптам на странице, а HttpOnly заодно чуть усложняет
		// её подмену из консоли браузера.
		HttpOnly: true,
		Secure:   r.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(r.cookieTTL.Seconds()),
	}
}
