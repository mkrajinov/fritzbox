// Package player plays audio files with the system's command-line player.
package player

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// candidates per OS, in order of preference. The file name is appended.
var candidates = map[string][][]string{
	"darwin": {{"afplay"}},
	"linux":  {{"paplay"}, {"pw-play"}, {"aplay", "-q"}, {"ffplay", "-nodisp", "-autoexit", "-loglevel", "quiet"}, {"mpv", "--really-quiet", "--no-video"}},
}

// Command returns the player command for this system. $FB_PLAYER (e.g. "mpv --no-video")
// overrides the choice.
func Command() ([]string, error) {
	if p := strings.Fields(os.Getenv("FB_PLAYER")); len(p) > 0 {
		return p, nil
	}
	for _, c := range candidates[runtime.GOOS] {
		if _, err := exec.LookPath(c[0]); err == nil {
			return c, nil
		}
	}
	if runtime.GOOS == "linux" {
		return nil, errors.New("no audio player found: install pulseaudio-utils (paplay), alsa-utils (aplay), ffmpeg or mpv, set $FB_PLAYER, or save the file with -o")
	}
	return nil, errors.New("no audio player found on " + runtime.GOOS + ": set $FB_PLAYER or save the file with -o")
}

// Play plays a file and returns when playback ends or ctx is cancelled.
func Play(ctx context.Context, path string) error {
	c, err := Command()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, c[0], append(c[1:], path)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(c[0] + ": " + msg)
		}
		return errors.New(c[0] + ": " + err.Error())
	}
	return nil
}

// PlayBytes writes data to a temporary file with the given extension, plays it and removes it.
func PlayBytes(ctx context.Context, data []byte, ext string) error {
	f, err := os.CreateTemp("", "fritzbox-*"+ext)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return Play(ctx, f.Name())
}
