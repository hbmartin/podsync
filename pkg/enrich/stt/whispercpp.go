package stt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/mxpv/podsync/pkg/feed"
)

// whisperCPP transcribes audio with a local whisper.cpp CLI binary.
type whisperCPP struct {
	binary    string
	modelPath string
	ffmpeg    string
	timeout   time.Duration
}

func newWhisperCPP(cfg *feed.STTProviderConfig, ffmpeg string, timeout time.Duration) (Provider, error) {
	if cfg.Binary == "" {
		return nil, errors.New("binary is required")
	}
	if cfg.ModelPath == "" {
		return nil, errors.New("model_path is required")
	}
	if ffmpeg == "" {
		return nil, errors.New("ffmpeg is required to prepare audio for whisper.cpp")
	}
	return &whisperCPP{
		binary:    cfg.Binary,
		modelPath: cfg.ModelPath,
		ffmpeg:    ffmpeg,
		timeout:   timeout,
	}, nil
}

func (p *whisperCPP) Name() string { return "whisper_cpp" }

func (p *whisperCPP) Transcribe(ctx context.Context, mediaPath, lang, outPath string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	// whisper.cpp expects 16kHz mono WAV input.
	wavPath := outPath + ".wav"
	defer os.Remove(wavPath)

	output, err := exec.CommandContext(ctx, p.ffmpeg,
		"-y", "-loglevel", "error",
		"-i", mediaPath,
		"-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le",
		wavPath,
	).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "ffmpeg audio conversion failed: %s", string(output))
	}

	// whisper.cpp writes "<output-base>.vtt" with --output-vtt.
	outBase := strings.TrimSuffix(outPath, filepath.Ext(outPath))
	args := []string{
		"--model", p.modelPath,
		"--output-vtt",
		"--output-file", outBase,
	}
	if lang != "" {
		args = append(args, "--language", lang)
	}
	args = append(args, wavPath)

	output, err = exec.CommandContext(ctx, p.binary, args...).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "whisper.cpp failed: %s", truncate(string(output), 512))
	}

	produced := outBase + ".vtt"
	if produced != outPath {
		if err := os.Rename(produced, outPath); err != nil {
			return errors.Wrap(err, "failed to move whisper.cpp output")
		}
	}

	if info, err := os.Stat(outPath); err != nil || info.Size() == 0 {
		return errors.New("whisper.cpp did not produce a transcript")
	}
	return nil
}
