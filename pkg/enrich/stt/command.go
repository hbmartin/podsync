package stt

import (
	"context"
	"os"

	"github.com/pkg/errors"

	"github.com/mxpv/podsync/pkg/feed"
)

// command delegates transcription to a user-configured command, following
// the same conventions as podsync's episode hooks. The command receives:
//
//	PODSYNC_AUDIO_FILE        input media file path
//	PODSYNC_TRANSCRIPT_OUTPUT WebVTT file path the command must create
//	PODSYNC_LANGUAGE          preferred language code (may be empty)
type command struct {
	hook feed.ExecHook
}

func newCommand(cfg *feed.STTProviderConfig) (Provider, error) {
	if len(cfg.Command) == 0 {
		return nil, errors.New("command is required")
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = int(DefaultTimeout.Seconds())
	}

	return &command{hook: feed.ExecHook{Command: cfg.Command, Timeout: timeout}}, nil
}

func (p *command) Name() string { return "command" }

func (p *command) Transcribe(_ context.Context, mediaPath, lang, outPath string) error {
	env := []string{
		"PODSYNC_AUDIO_FILE=" + mediaPath,
		"PODSYNC_TRANSCRIPT_OUTPUT=" + outPath,
		"PODSYNC_LANGUAGE=" + lang,
	}

	if err := p.hook.Invoke(env); err != nil {
		return err
	}

	if info, err := os.Stat(outPath); err != nil || info.Size() == 0 {
		return errors.New("command did not write a transcript file")
	}
	return nil
}
