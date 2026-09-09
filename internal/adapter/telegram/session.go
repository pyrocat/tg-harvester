package telegram

import (
	"context"
	"errors"

	"github.com/gotd/td/session"

	"tg-harvester/internal/domain"
)

// SessionStore persists the MTProto session blob.
type SessionStore interface {
	LoadSession(ctx context.Context) ([]byte, error)
	StoreSession(ctx context.Context, data []byte) error
}

// sessionStorage adapts a SessionStore to gotd's session.Storage, which
// signals "no session yet" with its own sentinel error.
type sessionStorage struct {
	store SessionStore
}

var _ session.Storage = sessionStorage{}

func (s sessionStorage) LoadSession(ctx context.Context) ([]byte, error) {
	data, err := s.store.LoadSession(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, session.ErrNotFound
	}
	return data, nil
}

func (s sessionStorage) StoreSession(ctx context.Context, data []byte) error {
	return s.store.StoreSession(ctx, data)
}
