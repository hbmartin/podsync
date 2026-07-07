// Package stt provides the speech-to-text fallback chain used to generate
// transcripts for episodes whose platform offers no subtitles.
package stt

import (
	"context"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/feed"
)

// DefaultTimeout bounds a single transcription attempt.
const DefaultTimeout = 30 * time.Minute

// Provider transcribes a media file into a WebVTT file.
type Provider interface {
	// Name identifies the provider in logs and transcript source labels.
	Name() string
	// Transcribe writes a WebVTT transcript of mediaPath to outPath.
	Transcribe(ctx context.Context, mediaPath, lang, outPath string) error
}

// NewChain builds the ordered provider chain from configuration.
func NewChain(cfgs []*feed.STTProviderConfig, ffmpeg string) ([]Provider, error) {
	providers := make([]Provider, 0, len(cfgs))
	for i, cfg := range cfgs {
		provider, err := newProvider(cfg, ffmpeg)
		if err != nil {
			return nil, errors.Wrapf(err, "invalid stt provider %d", i+1)
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

func newProvider(cfg *feed.STTProviderConfig, ffmpeg string) (Provider, error) {
	timeout := DefaultTimeout
	if cfg.Timeout > 0 {
		timeout = time.Duration(cfg.Timeout) * time.Second
	}

	switch cfg.Type {
	case feed.STTTypeOpenAI:
		return newOpenAI(cfg, timeout)
	case feed.STTTypeWhisperCPP:
		return newWhisperCPP(cfg, ffmpeg, timeout)
	case feed.STTTypeCommand:
		return newCommand(cfg)
	default:
		return nil, errors.Errorf("unknown stt provider type %q", cfg.Type)
	}
}

// Transcribe runs the chain in order, returning the name of the provider
// that succeeded.
func Transcribe(ctx context.Context, chain []Provider, mediaPath, lang, outPath string) (string, error) {
	if len(chain) == 0 {
		return "", errors.New("no stt providers configured")
	}

	var lastErr error
	for _, provider := range chain {
		log.Debugf("transcribing %q with stt provider %q", mediaPath, provider.Name())
		if err := provider.Transcribe(ctx, mediaPath, lang, outPath); err != nil {
			log.WithError(err).Warnf("stt provider %q failed", provider.Name())
			lastErr = err
			continue
		}
		return provider.Name(), nil
	}
	return "", errors.Wrap(lastErr, "all stt providers failed")
}
