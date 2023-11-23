package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

type CosignClient struct {
	exe string
	key string
}

func NewCosignClient(key string) *CosignClient {
	return &CosignClient{exe: "cosign", key: key}
}

func (c *CosignClient) Sign(imageRef string) error {
	args := append([]string{"sign"}, c.commonArgs(imageRef)...)
	return c.runCmd(append(args, imageRef)...)
}

func (c *CosignClient) Attest(imageRef, aType, aPredicate string) error {
	args := append([]string{"attest"}, c.commonArgs(imageRef)...)
	return c.runCmd(append(args, "--type", aType, "--predicate", aPredicate, imageRef)...)
}

func (c *CosignClient) commonArgs(imageRef string) []string {
	args := []string{"--tlog-upload=false", "--key", c.key}
	if strings.HasPrefix(imageRef, kindRegistry) {
		args = append(args, "--allow-insecure-registry=true", "--allow-http-registry=true")
	}
	return args
}

func (c *CosignClient) runCmd(args ...string) error {
	cmd := exec.Command(c.exe, args...)
	buf := new(bytes.Buffer)
	cmd.Stderr = buf
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("cosign cmd: %s - %s", err, buf.String())
	}
	return nil
}
