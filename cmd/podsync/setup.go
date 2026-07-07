package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/pelletier/go-toml"
	"github.com/pkg/errors"

	"github.com/mxpv/podsync/pkg/builder"
	"github.com/mxpv/podsync/pkg/model"
)

// SetupCommand is the "podsync setup" subcommand. It deliberately does not
// implement flags.Commander so that dispatch stays explicit in main.
type SetupCommand struct{}

const youtubeAPIKeyURL = "https://developers.google.com/youtube/registering_an_application"

var feedIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// prompter reads answers from r and writes questions to w.
type prompter struct {
	r *bufio.Reader
	w io.Writer
}

// ask prints the question and returns the entered line, or defaultValue if
// the input is empty.
func (p *prompter) ask(question, defaultValue string) (string, error) {
	if defaultValue != "" {
		fmt.Fprintf(p.w, "%s [%s]: ", question, defaultValue)
	} else {
		fmt.Fprintf(p.w, "%s: ", question)
	}

	line, err := p.r.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", errors.Wrap(err, "failed to read input")
	}

	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue, nil
	}

	return line, nil
}

// askRequired re-prompts until a non-empty answer is entered.
func (p *prompter) askRequired(question string) (string, error) {
	for {
		answer, err := p.ask(question, "")
		if err != nil {
			return "", err
		}
		if answer != "" {
			return answer, nil
		}
		fmt.Fprintln(p.w, "A value is required.")
	}
}

// askChoice re-prompts until the answer matches one of the given choices
// (case-insensitive); an empty answer selects the default.
func (p *prompter) askChoice(question string, choices []string, defaultValue string) (string, error) {
	prompt := fmt.Sprintf("%s (%s)", question, strings.Join(choices, "/"))
	for {
		answer, err := p.ask(prompt, defaultValue)
		if err != nil {
			return "", err
		}
		for _, choice := range choices {
			if strings.EqualFold(answer, choice) {
				return choice, nil
			}
		}
		fmt.Fprintf(p.w, "Please enter one of: %s\n", strings.Join(choices, ", "))
	}
}

// askYesNo asks a yes/no question; an empty answer selects the default.
func (p *prompter) askYesNo(question string, defaultYes bool) (bool, error) {
	hint := "y/N"
	def := "n"
	if defaultYes {
		hint = "Y/n"
		def = "y"
	}

	for {
		answer, err := p.ask(fmt.Sprintf("%s (%s)", question, hint), def)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(p.w, "Please answer y or n.")
	}
}

type setupFeed struct {
	ID       string
	URL      string
	Provider model.Provider
	Format   string
	Quality  string
}

type setupAnswers struct {
	Feeds         []setupFeed
	DataDir       string
	Port          int
	Hostname      string
	YouTubeAPIKey string
}

func (a *setupAnswers) hasProvider(provider model.Provider) bool {
	for _, f := range a.Feeds {
		if f.Provider == provider {
			return true
		}
	}
	return false
}

func (a *setupAnswers) HasYouTube() bool { return a.hasProvider(model.ProviderYoutube) }
func (a *setupAnswers) HasVimeo() bool   { return a.hasProvider(model.ProviderVimeo) }
func (a *setupAnswers) HasTwitch() bool  { return a.hasProvider(model.ProviderTwitch) }

func collectAnswers(p *prompter, configPath string) (*setupAnswers, error) {
	fmt.Fprintf(p.w, "Welcome to Podsync setup. This will generate %s.\n", configPath)
	fmt.Fprintln(p.w, "Press Enter to accept the [default] shown for each question.")

	answers := &setupAnswers{}

	// Feeds
	for {
		fmt.Fprintf(p.w, "\n-- Feed %d --\n", len(answers.Feeds)+1)

		var info model.Info
		for {
			feedURL, err := p.askRequired("Feed URL (YouTube/Vimeo/SoundCloud/Twitch channel, user, or playlist)")
			if err != nil {
				return nil, err
			}

			info, err = builder.ParseURL(feedURL)
			if err != nil {
				fmt.Fprintf(p.w, "Unsupported URL: %v\n", err)
				continue
			}

			answers.Feeds = append(answers.Feeds, setupFeed{URL: feedURL, Provider: info.Provider})
			break
		}
		current := &answers.Feeds[len(answers.Feeds)-1]

		for {
			id, err := p.ask("Feed ID — used in the feed link, e.g. http://localhost:8080/<id>.xml", fmt.Sprintf("feed%d", len(answers.Feeds)))
			if err != nil {
				return nil, err
			}
			if !feedIDPattern.MatchString(id) {
				fmt.Fprintln(p.w, "Feed ID may contain only letters, digits, '_' and '-', and must start with a letter or digit.")
				continue
			}
			duplicate := false
			for _, f := range answers.Feeds[:len(answers.Feeds)-1] {
				if f.ID == id {
					duplicate = true
					break
				}
			}
			if duplicate {
				fmt.Fprintf(p.w, "Feed ID %q is already used, pick another one.\n", id)
				continue
			}
			current.ID = id
			break
		}

		format, err := p.askChoice("Format", []string{"video", "audio"}, "video")
		if err != nil {
			return nil, err
		}
		current.Format = format

		quality, err := p.askChoice("Quality", []string{"high", "low"}, "high")
		if err != nil {
			return nil, err
		}
		current.Quality = quality

		more, err := p.askYesNo("Add another feed?", false)
		if err != nil {
			return nil, err
		}
		if !more {
			break
		}
	}

	// Storage
	fmt.Fprintln(p.w, "\n-- Storage --")
	dataDir, err := p.ask("Directory to store downloaded episodes", "./data")
	if err != nil {
		return nil, err
	}
	answers.DataDir = dataDir

	// Server
	fmt.Fprintln(p.w, "\n-- Server --")
	for {
		portStr, err := p.ask("HTTP port", "8080")
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			fmt.Fprintln(p.w, "Please enter a port number between 1 and 65535.")
			continue
		}
		answers.Port = port
		break
	}

	hostname, err := p.ask("Hostname for episode links", fmt.Sprintf("http://localhost:%d", answers.Port))
	if err != nil {
		return nil, err
	}
	answers.Hostname = hostname

	// API keys
	if answers.HasYouTube() {
		fmt.Fprintln(p.w, "\n-- API keys --")
		fmt.Fprintf(p.w, "YouTube feeds require an API key. Create one at:\n%s\n", youtubeAPIKeyURL)
		key, err := p.ask("YouTube API key (leave empty to set later via PODSYNC_YOUTUBE_API_KEY)", "")
		if err != nil {
			return nil, err
		}
		answers.YouTubeAPIKey = key
	}

	return answers, nil
}

const setupTemplate = `# Podsync configuration file, generated by "podsync setup".
# See config.toml.example for the full list of available options:
# https://github.com/mxpv/podsync/blob/main/config.toml.example

[server]
port = {{ .Port }}
hostname = {{ tomlString .Hostname }}

[storage]
type = "local"

[storage.local]
data_dir = {{ tomlString .DataDir }}

[tokens]
{{- if .YouTubeAPIKey }}
youtube = {{ tomlString .YouTubeAPIKey }}
{{- else if .HasYouTube }}
# Create a YouTube API key at {{ .YouTubeAPIKeyURL }}
# and set it here or via the PODSYNC_YOUTUBE_API_KEY environment variable.
# youtube = "YOUR_API_KEY"
{{- end }}
{{- if .HasVimeo }}
# Vimeo feeds require an access token, see https://developer.vimeo.com/api/guides/start
# vimeo = "YOUR_ACCESS_TOKEN"
{{- end }}
{{- if .HasTwitch }}
# Twitch feeds require client credentials, see https://dev.twitch.tv/console/apps
# twitch = "CLIENT_ID:CLIENT_SECRET"
{{- end }}
{{ range .Feeds }}
[feeds.{{ .ID }}]
url = {{ tomlString .URL }}
format = {{ tomlString .Format }} # "video" or "audio"
quality = {{ tomlString .Quality }} # "high" or "low"
# update_period = "6h" # how often to check for new episodes
{{ end -}}
`

type setupTemplateData struct {
	*setupAnswers
	YouTubeAPIKeyURL string
}

// renderConfig renders the TOML config and verifies that the output parses
// back into the expected configuration.
func renderConfig(answers *setupAnswers) ([]byte, error) {
	tmpl, err := template.New("config").Funcs(template.FuncMap{
		"tomlString": func(s string) string { return strconv.Quote(s) },
	}).Parse(setupTemplate)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse config template")
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, setupTemplateData{setupAnswers: answers, YouTubeAPIKeyURL: youtubeAPIKeyURL}); err != nil {
		return nil, errors.Wrap(err, "failed to render config")
	}

	data := []byte(buf.String())

	// Self-check: the generated file must parse back into the expected config.
	var check Config
	if err := toml.Unmarshal(data, &check); err != nil {
		return nil, errors.Wrap(err, "generated config failed to parse, this is a bug in podsync setup")
	}
	if len(check.Feeds) != len(answers.Feeds) {
		return nil, errors.New("generated config lost feeds, this is a bug in podsync setup")
	}
	for _, f := range answers.Feeds {
		parsed, ok := check.Feeds[f.ID]
		if !ok || parsed.URL != f.URL {
			return nil, errors.Errorf("generated config is missing feed %q, this is a bug in podsync setup", f.ID)
		}
	}

	return data, nil
}

func runSetup(configPath string, stdin io.Reader, stdout io.Writer) error {
	p := &prompter{r: bufio.NewReader(stdin), w: stdout}

	if _, err := os.Stat(configPath); err == nil {
		overwrite, err := p.askYesNo(fmt.Sprintf("%s already exists. Overwrite?", configPath), false)
		if err != nil {
			return err
		}
		if !overwrite {
			return errors.New("aborted, existing configuration left untouched")
		}
	}

	answers, err := collectAnswers(p, configPath)
	if err != nil {
		return err
	}

	data, err := renderConfig(answers)
	if err != nil {
		return err
	}

	// The file may contain an API key, so restrict permissions
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return errors.Wrapf(err, "failed to write %s", configPath)
	}

	fmt.Fprintf(p.w, "\nWrote %s.\n", configPath)
	if answers.HasVimeo() || answers.HasTwitch() {
		fmt.Fprintln(p.w, "Note: Vimeo/Twitch feeds require API tokens — see the commented [tokens] section in the generated file.")
	}
	fmt.Fprintf(p.w, "Next: run podsync --config %s\n", configPath)

	return nil
}
