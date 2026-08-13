//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"strconv"

	client "github.com/PeterSR/pupptyeer/clients/go"
)

func ctlCommandList() string {
	return "list|new|send|capture|attach|expect|resize|kill|gc|steal"
}

func ctlSteal(c *client.Client, opts stealOptions, cfg config) (string, error) {
	reptyr, err := exec.LookPath("reptyr")
	if err != nil {
		return "", fmt.Errorf("ctl steal requires reptyr in PATH")
	}
	args := []string{}
	if opts.tty {
		args = append(args, "-T")
	}
	args = append(args, strconv.Itoa(opts.pid))
	var sessionOpts []client.SessionOption
	if opts.id != "" {
		sessionOpts = append(sessionOpts, client.WithSessionID(opts.id))
	}
	return c.NewSession(reptyr, args, "", map[string]string{"TERM": "xterm-256color"}, cfg.defaultCols, cfg.defaultRows, sessionOpts...)
}
