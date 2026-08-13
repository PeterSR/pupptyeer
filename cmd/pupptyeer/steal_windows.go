//go:build windows

package main

import (
	"errors"

	client "github.com/PeterSR/pupptyeer/clients/go"
)

func ctlCommandList() string {
	return "list|new|send|capture|attach|expect|resize|kill|gc"
}

func ctlSteal(_ *client.Client, _ stealOptions, _ config) (string, error) {
	return "", errors.New("ctl steal is unavailable on windows")
}
