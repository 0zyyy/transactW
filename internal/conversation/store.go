package conversation

import (
	"sync"
	"time"

	"transactw/internal/inference"
)

type PendingDraft struct {
	ConversationKey string
	Parsed          inference.ParseTextResponse
	CreatedAt       time.Time
	ExpiresAt       time.Time
}

type DraftStore interface {
	Save(conversationKey string, parsed inference.ParseTextResponse) (PendingDraft, error)
	Confirm(conversationKey string) (PendingDraft, bool, error)
	Cancel(conversationKey string) (bool, error)
}

type Store struct {
	mu     sync.Mutex
	ttl    time.Duration
	drafts map[string]PendingDraft
}

func NewStore(ttl time.Duration) *Store {
	return &Store{
		ttl:    ttl,
		drafts: make(map[string]PendingDraft),
	}
}

func (s *Store) Save(conversationKey string, parsed inference.ParseTextResponse) (PendingDraft, error) {
	now := time.Now()
	draft := PendingDraft{
		ConversationKey: conversationKey,
		Parsed:          parsed,
		CreatedAt:       now,
		ExpiresAt:       now.Add(s.ttl),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	s.drafts[conversationKey] = draft
	return draft, nil
}

func (s *Store) Get(conversationKey string) (PendingDraft, bool, error) {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)

	draft, ok := s.drafts[conversationKey]
	if !ok {
		return PendingDraft{}, false, nil
	}
	if now.After(draft.ExpiresAt) {
		delete(s.drafts, conversationKey)
		return PendingDraft{}, false, nil
	}
	return draft, true, nil
}

func (s *Store) Confirm(conversationKey string) (PendingDraft, bool, error) {
	draft, ok, err := s.Get(conversationKey)
	if err != nil || !ok {
		return PendingDraft{}, ok, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.drafts, conversationKey)
	return draft, true, nil
}

func (s *Store) Cancel(conversationKey string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.drafts[conversationKey]
	delete(s.drafts, conversationKey)
	return ok, nil
}

func (s *Store) cleanupLocked(now time.Time) {
	for key, draft := range s.drafts {
		if now.After(draft.ExpiresAt) {
			delete(s.drafts, key)
		}
	}
}

func IsDraftIntent(intent string) bool {
	switch intent {
	case "create_expense", "create_income", "create_multiple_transactions":
		return true
	default:
		return false
	}
}
