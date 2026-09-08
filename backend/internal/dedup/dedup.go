// Package dedup отвечает за вопрос «этот зритель уже голосовал?».
//
// Защита намеренно бытового уровня: она останавливает повторное нажатие,
// перезагрузку страницы и попытку проголосовать ещё раз с того же телефона,
// но не человека, который откроет инкогнито или сменит сеть. Так и задумано —
// требовалась дедупликация «на уровне обычных пользователей», а не защита от
// целенаправленной накрутки, которая без регистрации всё равно недостижима.
package dedup

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/netip"
	"strings"

	"github.com/google/uuid"
)

// signatureLen — сколько байт HMAC кладём в куку. 128 бит достаточно, чтобы
// подпись нельзя было подобрать, и вдвое короче полного SHA-256.
const signatureLen = 16

// keyHashLen — длина хэша в ключе Redis. При 20М ключей на опрос вероятность
// коллизии на 128 битах пренебрежимо мала, зато ключ вдвое легче.
const keyHashLen = 16

var encoding = base64.RawURLEncoding

// Identity — то, чем мы считаем «одного зрителя».
type Identity struct {
	// Value участвует в ключе дедупликации.
	Value string
	// FromCookie=false означает, что зритель пришёл без рабочей куки и опознан
	// по сети и User-Agent — заметно грубее.
	FromCookie bool
}

// Signer выпускает и проверяет подписанные куки голосующего.
type Signer struct {
	secret []byte
}

// NewSigner создаёт подписывальщик кук.
func NewSigner(secret string) *Signer {
	return &Signer{secret: []byte(secret)}
}

// Issue выдаёт новый идентификатор зрителя и значение куки для него.
func (s *Signer) Issue() (voterID uuid.UUID, cookieValue string) {
	voterID = uuid.New()

	return voterID, s.sign(voterID)
}

// Verify разбирает значение куки и возвращает идентификатор, если подпись цела.
//
// Подпись нужна не ради защиты голосов, а чтобы кука не превращалась в
// произвольную строку от клиента: иначе любой мог бы слать случайный vid
// в каждом запросе и голосовать бесконечно, не трогая браузер.
func (s *Signer) Verify(cookieValue string) (uuid.UUID, bool) {
	rawID, rawSignature, found := strings.Cut(cookieValue, ".")
	if !found {
		return uuid.Nil, false
	}

	idBytes, err := encoding.DecodeString(rawID)
	if err != nil || len(idBytes) != len(uuid.UUID{}) {
		return uuid.Nil, false
	}

	signature, err := encoding.DecodeString(rawSignature)
	if err != nil {
		return uuid.Nil, false
	}

	voterID := uuid.UUID(idBytes)
	if !hmac.Equal(signature, s.signature(voterID)) {
		return uuid.Nil, false
	}

	return voterID, true
}

func (s *Signer) sign(voterID uuid.UUID) string {
	return encoding.EncodeToString(voterID[:]) + "." + encoding.EncodeToString(s.signature(voterID))
}

func (s *Signer) signature(voterID uuid.UUID) []byte {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(voterID[:])

	return mac.Sum(nil)[:signatureLen]
}

// CookieIdentity — личность зрителя, опознанного по куке.
func CookieIdentity(voterID uuid.UUID) Identity {
	return Identity{Value: "v:" + voterID.String(), FromCookie: true}
}

// FingerprintIdentity — запасной вариант для зрителей без рабочей куки:
// подсеть плюс User-Agent.
//
// За общим NAT такие зрители могут схлопнуться в одного и получить отказ, но
// это касается только тех, у кого куки отключены целиком.
func FingerprintIdentity(remoteIP, userAgent string) Identity {
	return Identity{Value: "f:" + NormalizeIP(remoteIP) + "|" + userAgent}
}

// Key возвращает ключ Redis, по которому отмечено, что зритель проголосовал.
//
// Хэш идентичности стоит в конце ключа целиком, без hash-тегов: так ключи
// одного опроса равномерно расходятся по слотам Redis Cluster и опрос не
// упирается в одну ноду.
func Key(pollID uuid.UUID, identity Identity) string {
	sum := sha256.Sum256([]byte(pollID.String() + "|" + identity.Value))

	return "vote:d:" + pollID.String() + ":" + hex.EncodeToString(sum[:keyHashLen])
}

// NormalizeIP огрубляет адрес до подсети: /24 для IPv4 и /64 для IPv6.
//
// Точный адрес меняется у мобильных клиентов слишком часто, чтобы на него
// опираться, а подсеть держится стабильнее.
func NormalizeIP(remoteIP string) string {
	host := remoteIP
	if h, _, err := net.SplitHostPort(remoteIP); err == nil {
		host = h
	}

	addr, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return remoteIP
	}

	bits := 64
	if addr.Is4() {
		bits = 24
	}

	prefix, err := addr.Prefix(bits)
	if err != nil {
		return addr.String()
	}

	return prefix.String()
}
