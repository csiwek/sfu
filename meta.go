package sfu

import (
	"errors"
	"sync"
)

var (
	ErrMetaNotFound = errors.New("meta: metadata not found")
)

type Metadata struct {
	mu                 sync.RWMutex
	m                  map[string]interface{}
	onChangedCallbacks []func(key string, value interface{})
}

type OnMetaChangedSubscription struct {
	meta *Metadata
	idx  int
}

// Unsubscribe removes the callback from the metadata
// Make sure to call the method once the callback is no longer needed
func (s *OnMetaChangedSubscription) Unsubscribe() {
	s.meta.mu.Lock()
	defer s.meta.mu.Unlock()

	s.meta.onChangedCallbacks = append(s.meta.onChangedCallbacks[:s.idx], s.meta.onChangedCallbacks[s.idx+1:]...)
}

func NewMetadata() *Metadata {
	return &Metadata{
		mu:                 sync.RWMutex{},
		m:                  make(map[string]interface{}),
		onChangedCallbacks: make([]func(key string, value interface{}), 0),
	}
}

func (m *Metadata) Set(key string, value interface{}) {
	m.mu.Lock()
	m.m[key] = value
	m.mu.Unlock()

	m.onChanged(key, value)
}

func (m *Metadata) Get(key string) (interface{}, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, ok := m.m[key]; !ok {
		return nil, ErrMetaNotFound
	}
	return m.m[key], nil
}

func (m *Metadata) Delete(key string) error {
	m.mu.Lock()
	if _, ok := m.m[key]; !ok {
		m.mu.Unlock()
		return ErrMetaNotFound
	}

	delete(m.m, key)
	m.mu.Unlock()

	m.onChanged(key, nil)

	return nil
}

func (m *Metadata) ForEach(f func(key string, value interface{})) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for k, v := range m.m {
		f(k, v)
	}
}

func (m *Metadata) onChanged(key string, value interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, f := range m.onChangedCallbacks {
		f(key, value)
	}
}

// OnChanged registers a callback to be called when a metadata is changed
// Make sure OnMetaChangedSubscription.Unsubscribe() is called when the callback is no longer needed
func (m *Metadata) OnChanged(f func(key string, value interface{})) *OnMetaChangedSubscription {
	m.mu.Lock()
	nextIdx := len(m.onChangedCallbacks)
	m.onChangedCallbacks = append(m.onChangedCallbacks, f)
	m.mu.Unlock()

	sub := &OnMetaChangedSubscription{
		meta: m,
		idx:  nextIdx,
	}

	return sub
}
