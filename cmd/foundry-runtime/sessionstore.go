package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/AltairaLabs/PromptKit/runtime/statestore"
	"github.com/AltairaLabs/PromptKit/runtime/statestore/file"
	"github.com/redis/go-redis/v9"
)

// Conversation history has to outlive one request.
//
// PromptKit builds a fresh in-process store for every Open when none is
// supplied, and this runtime opens a conversation per request, so without a
// store handed in nothing is ever remembered -- not across a restart, and not
// even across two turns of the same conversation.
//
// Which store fits depends on the deployment, so the choice is the operator's:
//
//	memory  one store for the process. Survives between turns served by the
//	        same container, and nothing more. No infrastructure.
//	file    a directory the platform persists per session. Survives the
//	        sandbox being suspended and restored.
//	redis   an external store. The only one that holds up when more than one
//	        container serves the same conversation.
const (
	storeKindMemory = "memory"
	storeKindFile   = "file"
	storeKindRedis  = "redis"
)

// sessionStateTTL sweeps conversation state the platform would drop anyway: a
// session lives at most 30 days.
const sessionStateTTL = 30 * 24 * time.Hour

// defaultStoreDirName is where file state goes when no root is configured.
const defaultStoreDirName = "sessions"

// newSessionStore builds the configured store.
//
// A store that cannot be built is reported and the agent serves without one:
// it answers but does not remember. Refusing to start would trade a degraded
// deployment for no deployment, and the operator can see the warning.
func newSessionStore(cfg *runtimeConfig, log *slog.Logger) statestore.Store {
	store, err := buildSessionStore(cfg)
	if err != nil {
		log.Warn("conversations will not be remembered between turns",
			"store", cfg.StateStoreKind, "error", err)
		return nil
	}

	log.Info("conversation state configured", "store", cfg.StateStoreKind)
	return store
}

// buildSessionStore constructs the store named by the config.
func buildSessionStore(cfg *runtimeConfig) (statestore.Store, error) {
	switch cfg.StateStoreKind {
	case "", storeKindMemory:
		// One store for the process, not one per conversation: the whole point
		// is that two turns of a conversation find each other.
		return statestore.NewMemoryStore(), nil
	case storeKindFile:
		return newFileStore(cfg.StateStoreRoot)
	case storeKindRedis:
		return newRedisStore(cfg.StateStoreURL)
	default:
		return nil, fmt.Errorf(
			"unknown state store %q; expected %s, %s or %s",
			cfg.StateStoreKind, storeKindMemory, storeKindFile, storeKindRedis)
	}
}

// newFileStore opens a file-backed store.
//
// The configured root wins. Otherwise it goes under $HOME, which is the part of
// a session sandbox the platform persists across turns and idle periods.
func newFileStore(root string) (statestore.Store, error) {
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory for session state: %w", err)
		}
		root = filepath.Join(home, defaultStoreDirName)
	}

	store, err := file.NewStore(file.Options{Root: root, TTL: sessionStateTTL})
	if err != nil {
		return nil, fmt.Errorf("open file state store at %s: %w", root, err)
	}
	return store, nil
}

// newRedisStore connects an external store.
//
// The URL carries a credential, so it arrives through the environment rather
// than the deploy config: the adapter names the variable and never holds the
// secret itself.
func newRedisStore(url string) (statestore.Store, error) {
	if url == "" {
		return nil, fmt.Errorf(
			"state store %q needs a connection URL; none was injected", storeKindRedis)
	}

	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis URL: %w", err)
	}
	return statestore.NewRedisStore(redis.NewClient(opts), statestore.WithTTL(sessionStateTTL)), nil
}
