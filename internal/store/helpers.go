package store

import (
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// wrapNotFound преобразует pgx.ErrNoRows в ErrNotFound.
func wrapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// itoa — strconv без лишнего импорта в файлах.
func itoa(i int) string { return strconv.Itoa(i) }
