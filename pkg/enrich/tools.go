package enrich

import (
	"context"
	"os"
	"os/exec"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/feed"
)

// Toolset holds the resolved paths of the optional external helper tools.
// An empty field means the tool is unavailable; the corresponding feature
// degrades gracefully.
type Toolset struct {
	Transcript2JSON string
	PodcastChapters string
	VideoToChapters string
	FFmpeg          string
}

// ResolveTools looks up the configured helper binaries. Missing optional
// tools are logged once at startup rather than treated as errors.
func ResolveTools(cfg feed.ToolsConfig) Toolset {
	cfg.ApplyDefaults()

	tools := Toolset{
		Transcript2JSON: resolveBinary(cfg.Transcript2JSON),
		PodcastChapters: resolveBinary(cfg.PodcastChapters),
		VideoToChapters: resolveBinary(cfg.VideoToChapters),
		FFmpeg:          resolveBinary(cfg.FFmpeg),
	}

	logTool := func(name, path, feature string) {
		if path != "" {
			log.Debugf("found %s at %q", name, path)
		} else {
			log.Warnf("%s not found, %s", name, feature)
		}
	}

	logTool("transcript2json", tools.Transcript2JSON, "will use built-in VTT to transcript JSON conversion")
	logTool("podcast-chapters", tools.PodcastChapters, "will use built-in description chapter parsing")
	logTool("video-to-chapters-with-transcript", tools.VideoToChapters, "AI chapter generation is unavailable")
	logTool("ffmpeg", tools.FFmpeg, "chapter images and MP4 chapter embedding are unavailable")

	return tools
}

func resolveBinary(name string) string {
	if name == "" {
		return ""
	}
	if strings.ContainsRune(name, os.PathSeparator) {
		if _, err := os.Stat(name); err != nil {
			return ""
		}
		return name
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
}

// runTool executes an external helper with a timeout, returning combined
// output for error reporting.
func runTool(ctx context.Context, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}
