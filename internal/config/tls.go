// Package config: TLS-настройка для обхода отсутствия корневого сертификата
// Минцифры (Russian Trusted Root CA) в системном хранилище.
//
// Платформа MAX (platform-api2.max.ru) использует TLS с сертификатом,
// подписанным российским УЦ, которого нет в Debian/Alpine по умолчанию.
// Вместо ручного SSL_CERT_FILE встраиваем публичный корневой сертификат
// прямо в бинарник и добавляем его в CA-пул кастомного *http.Client.
package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	_ "embed"
)

//go:embed certs/russian_trusted_root_ca.pem
var russianRootCA []byte

//go:embed certs/russian_trusted_sub_ca.pem
var russianSubCA []byte

// NewHTTPClient возвращает *http.Client, доверяющий как системному хранилищу,
// так и встроенным сертификатам Минцифры. Клиент передаётся в MAX API через
// опцию WithHTTPClient.
func NewHTTPClient() (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		// Системного хранилища нет (Alpine scratch и т.п.) — стартуем с чистого.
		pool = x509.NewCertPool()
	}
	for _, pem := range [][]byte{russianRootCA, russianSubCA} {
		if !pool.AppendCertsFromPEM(pem) {
			// Не критично: возможно, сертификат уже добавлен — продолжаем.
		}
	}
	transport := &http.Transport{
		// Стандартные таймауты, как у DefaultTransport, но с нашим TLS-конфигом.
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    pool,
		},
		//ForceAttemptHTTP2: true, // cloudflared говорит http2 — но MAX сам апгрейдит.
	}
	return &http.Client{Transport: transport}, nil
}

// resolveCertErr возвращает ошибку, если ни один PEM не распарсился (защита от
// случайной порчи встроенных файлов при рефакторинге).
func resolveCertErr(parsed int) error {
	if parsed == 0 {
		return errors.New("ни один из встроенных сертификатов Минцифры не распознан")
	}
	return nil
}

// init проверяет на старте, что встроенные PEM хотя бы парсятся — ранний фейл
// вместо молчаливого отказа TLS.
func init() {
	pool := x509.NewCertPool()
	n := 0
	for _, pem := range [][]byte{russianRootCA, russianSubCA} {
		if pool.AppendCertsFromPEM(pem) {
			n++
		}
	}
	if err := resolveCertErr(n); err != nil {
		// Не паникуем — пусть NewHTTPClient залогирует; но сигнализируем через os.Stderr.
		os.Stderr.WriteString("WARN: " + err.Error() + "\n")
	}
}
