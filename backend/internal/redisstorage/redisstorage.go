// Package redisstorage создаёт клиент Redis для горячего пути голосования.
package redisstorage

import (
	"context"
	"fmt"

	"github.com/redis/rueidis"
)

// Options — параметры подключения.
type Options struct {
	Addrs    []string
	Username string
	Password string
}

// New создаёт клиент rueidis.
//
// rueidis выбран из-за автоматического пайплайнинга: конкурентные запросы
// склеиваются в один пакет на соединении, поэтому на голос приходится
// заметно меньше системных вызовов, чем при схеме «одно соединение из пула
// на запрос». Он же сам разбирается с топологией Redis Cluster, если адресов
// в конфиге несколько.
func New(ctx context.Context, opts Options) (rueidis.Client, error) {
	client, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress: opts.Addrs,
		Username:    opts.Username,
		Password:    opts.Password,
		// Счётчики и дедуп-ключи меняются постоянно, кэш на стороне клиента
		// тут только мешал бы: определение опроса мы кэшируем сами.
		DisableCache: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create redis client: %w", err)
	}

	if err := client.Do(ctx, client.B().Ping().Build()).Error(); err != nil {
		client.Close()

		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}
