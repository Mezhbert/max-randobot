package store

import "github.com/jackc/pgx/v5/pgxpool"

// Store объединяет все репозитории и держит пул соединений.
type Store struct {
	pool *pgxpool.Pool
}

// New создаёт Store поверх готового пула соединений.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool возвращает исходный пул (для нестандартных запросов).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }
